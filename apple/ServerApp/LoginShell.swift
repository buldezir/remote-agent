import Darwin
import Foundation

/// The environment rad gets: what the user's login shell sets up, laid over
/// the app's own. Apps start with launchd's PATH, which has none of the
/// places agent CLIs and node are installed (Homebrew, ~/.local/bin, nvm…).
enum LoginShell {
    /// Variables that describe the shell or this app, not the user's setup.
    private static let dropped: Set<String> = [
        "PWD", "OLDPWD", "SHLVL", "_", "COLORTERM", "__CFBundleIdentifier", "XPC_SERVICE_NAME", "XPC_FLAGS",
    ]
    /// Set on the app (e.g. `open --env RAD_HOME=…`), these win over the shell's.
    private static let fromApp = ["RAD_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"]
    private static let marker = "__RAD_ENV__"

    /// Runs the login shell once, interactively first so it reads ~/.zshrc too.
    nonisolated static func environment() async -> [String: String] {
        await Task.detached { resolve(base: ProcessInfo.processInfo.environment) }.value
    }

    nonisolated private static func resolve(base: [String: String]) -> [String: String] {
        let shell = base["SHELL"].flatMap { $0.isEmpty ? nil : $0 } ?? "/bin/zsh"
        var env = base
        if let found = capture(shell: shell, interactive: true, env: base) ?? capture(shell: shell, interactive: false, env: base) {
            env.merge(found) { _, shell in shell }
        } else {
            let home = FileManager.default.homeDirectoryForCurrentUser.path
            env["PATH"] = [base["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin", "/opt/homebrew/bin", "/usr/local/bin", "\(home)/.local/bin"]
                .joined(separator: ":")
        }
        for key in fromApp where base[key] != nil {
            env[key] = base[key]
        }
        return env.filter { key, _ in !dropped.contains(key) && !key.hasPrefix("TERM") }
    }

    /// Runs `shell -l [-i] -c 'env -0'` in its own process group with no
    /// terminal, and kills the group if it takes over 10 seconds (an rc file
    /// waiting for input, say).
    nonisolated private static func capture(shell: String, interactive: Bool, env: [String: String]) -> [String: String]? {
        var fds: [Int32] = [0, 0]
        guard pipe(&fds) == 0 else { return nil }
        let (readEnd, writeEnd) = (fds[0], fds[1])

        var attr: posix_spawnattr_t?
        posix_spawnattr_init(&attr)
        defer { posix_spawnattr_destroy(&attr) }
        // CLOEXEC_DEFAULT: the shell must not inherit rad's stdin pipe, or a
        // daemon started from an rc file would keep rad alive after the app.
        posix_spawnattr_setflags(&attr, Int16(POSIX_SPAWN_SETPGROUP | POSIX_SPAWN_CLOEXEC_DEFAULT))
        posix_spawnattr_setpgroup(&attr, 0)
        var actions: posix_spawn_file_actions_t?
        posix_spawn_file_actions_init(&actions)
        defer { posix_spawn_file_actions_destroy(&actions) }
        posix_spawn_file_actions_addopen(&actions, 0, "/dev/null", O_RDONLY, 0)
        posix_spawn_file_actions_adddup2(&actions, writeEnd, 1)
        posix_spawn_file_actions_addopen(&actions, 2, "/dev/null", O_WRONLY, 0)

        let script = "printf '\\0\(marker)\\0'; exec /usr/bin/env -0"
        let args = [shell, "-l"] + (interactive ? ["-i"] : []) + ["-c", script]
        let argv = args.map { strdup($0) } + [nil]
        let envp = env.map { strdup("\($0)=\($1)") } + [nil]
        defer {
            argv.forEach { free($0) }
            envp.forEach { free($0) }
        }
        var pid: pid_t = 0
        let spawned = posix_spawn(&pid, shell, &actions, &attr, argv, envp)
        close(writeEnd)
        guard spawned == 0 else {
            close(readEnd)
            return nil
        }

        var output = Data()
        var buffer = [UInt8](repeating: 0, count: 65536)
        let deadline = Date().addingTimeInterval(10)
        while true {
            let ms = Int32(deadline.timeIntervalSinceNow * 1000)
            var poller = pollfd(fd: readEnd, events: Int16(POLLIN), revents: 0)
            guard ms > 0, poll(&poller, 1, ms) > 0 else { break }
            let n = read(readEnd, &buffer, buffer.count)
            guard n > 0 else { break }
            output.append(buffer, count: n)
        }
        close(readEnd)
        kill(-pid, SIGKILL) // whatever the shell left running
        var status: Int32 = 0
        waitpid(pid, &status, 0)

        guard let start = output.range(of: Data("\0\(marker)\0".utf8)) else { return nil }
        var found: [String: String] = [:]
        for entry in output[start.upperBound...].split(separator: 0) {
            let pair = String(decoding: entry, as: UTF8.self)
            if let eq = pair.firstIndex(of: "="), eq != pair.startIndex {
                found[String(pair[..<eq])] = String(pair[pair.index(after: eq)...])
            }
        }
        return found["PATH"] == nil ? nil : found
    }
}
