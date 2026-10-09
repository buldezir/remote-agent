import AVFoundation
import Observation
import OSLog
import Speech
import UIKit

private let log = Logger(subsystem: "dev.remote-agent.app", category: "dictation")

/// Live speech-to-text for the composer, on device with the models behind
/// keyboard dictation. It listens in each of the user's keyboard languages (up
/// to three) and keeps the one it understood best.
@MainActor @Observable
final class Dictation {
    enum Phase { case idle, starting, downloading, listening, finishing }

    private(set) var phase = Phase.idle
    var error: String?

    @ObservationIgnored private var engine: AVAudioEngine?
    @ObservationIgnored private var live: LiveTranscription?
    // Bumped by every start and by stop, so a start that was stopped midway gives up.
    @ObservationIgnored private var attempt = 0

    /// Starts listening. onText gets the whole transcript so far each time it
    /// changes, through the final pass after stop(). hints are words to expect,
    /// such as the project's name.
    func start(hints: [String], onText: @escaping @MainActor (String) -> Void) async {
        guard phase == .idle else { return }
        attempt += 1
        let attempt = attempt
        phase = .starting
        var live: LiveTranscription?
        do {
            guard await Self.authorize() else { throw DictationError.notAllowed }
            let wanted = await languages()
            guard let first = wanted.first else { throw DictationError.noLanguage }
            var locales = await LiveTranscription.usable(wanted)
            if locales.isEmpty, self.attempt == attempt {
                phase = .downloading
                guard try await LiveTranscription.install(first) else { throw DictationError.noModel }
                locales = await LiveTranscription.usable([first])
            }
            guard self.attempt == attempt else { return }
            guard !locales.isEmpty else { throw DictationError.noModel }
            log.info("listening in \(locales.map(\.identifier).joined(separator: " "), privacy: .public)")
            live = try await LiveTranscription(locales: locales, hints: hints, onText: onText) { [weak self] error in
                self?.failed(error)
            }
            guard let live, self.attempt == attempt else {
                await live?.cancel()
                return
            }
            try startAudio(feeding: live.feed)
            self.live = live
            phase = .listening
        } catch {
            stopAudio()
            await live?.cancel()
            guard self.attempt == attempt else { return }
            phase = .idle
            self.error = error.localizedDescription
        }
    }

    /// Stops listening. The transcript still gets its final pass, which
    /// corrects the last words and settles the language.
    func stop() {
        switch phase {
        case .idle, .finishing:
            return
        case .starting, .downloading:
            attempt += 1
            phase = .idle
        case .listening:
            stopAudio()
            guard let live else {
                phase = .idle
                return
            }
            phase = .finishing
            Task {
                preferred = await live.finish()
                if self.live === live {
                    self.live = nil
                    phase = .idle
                }
            }
        }
    }

    private func failed(_ error: any Error) {
        guard phase == .listening else { return }
        self.error = error.localizedDescription
        stop()
    }

    /// The language of the last dictation, tried first next time.
    private var preferred: Locale? {
        get { UserDefaults.standard.string(forKey: "dictationLanguage").map(Locale.init(identifier:)) }
        set { UserDefaults.standard.set(newValue?.identifier, forKey: "dictationLanguage") }
    }

    /// The keyboard languages that dictation supports, one locale per language
    /// and the preferred one first. Falls back to the app's language.
    private func languages() async -> [Locale] {
        var out: [Locale] = []
        for id in UITextInputMode.activeInputModes.compactMap(\.primaryLanguage) {
            // "emoji" and the like map to nothing.
            guard let locale = await DictationTranscriber.supportedLocale(equivalentTo: Locale(identifier: id)),
                  !out.contains(where: { $0.language.languageCode == locale.language.languageCode })
            else { continue }
            out.append(locale)
        }
        if out.isEmpty, let locale = await DictationTranscriber.supportedLocale(equivalentTo: .current) {
            out = [locale]
        }
        if let preferred, let i = out.firstIndex(where: { $0.language.languageCode == preferred.language.languageCode }) {
            out.insert(out.remove(at: i), at: 0)
        }
        // Each language runs its own model.
        return Array(out.prefix(3))
    }

    private func startAudio(feeding feed: AudioFeed) throws {
        let session = AVAudioSession.sharedInstance()
        // The default mode keeps the system's input processing (gain, noise),
        // which .measurement turns off.
        try session.setCategory(.record, mode: .default, options: .duckOthers)
        try session.setActive(true, options: .notifyOthersOnDeactivation)
        let engine = AVAudioEngine()
        self.engine = engine
        try Self.installTap(on: engine.inputNode, feed: feed)
        engine.prepare()
        try engine.start()
    }

    private func stopAudio() {
        guard let engine else { return }
        self.engine = nil
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    // The audio tap runs on a background thread, so it is built outside the
    // main actor (a main-actor closure would trap there).
    private nonisolated static func installTap(on input: AVAudioInputNode, feed: AudioFeed) throws {
        let format = input.outputFormat(forBus: 0)
        guard format.channelCount > 0, format.sampleRate > 0 else {
            throw DictationError.noMicrophone
        }
        try input.installAudioTap(onBus: 0, bufferSize: AVAudioFrameCount(format.sampleRate / 10), format: format) { buffer, _ in
            feed.append(AVAudioPCMBuffer(copying: buffer))
        }
    }

    private nonisolated static func authorize() async -> Bool {
        let speech = await withCheckedContinuation { c in
            SFSpeechRecognizer.requestAuthorization { c.resume(returning: $0) }
        }
        guard speech == .authorized else { return false }
        return await AVAudioApplication.requestRecordPermission()
    }
}
