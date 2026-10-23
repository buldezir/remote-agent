import Foundation

extension SlashCommand {
    /// Only complete the leading command token; arguments and prose stay untouched.
    public static func query(_ draft: String) -> String? {
        guard draft.first == "/", !draft.contains(where: \.isWhitespace) else { return nil }
        return draft.dropFirst().lowercased()
    }

    public static func suggestions(for draft: String, from commands: [Self]) -> [Self] {
        guard let query = query(draft) else { return [] }
        var seen = Set<String>()
        return commands.enumerated().compactMap { index, command -> (Self, Int, Int)? in
            guard seen.insert(command.name).inserted else { return nil }
            let name = command.name.lowercased()
            let short = String(name.split(separator: ":").last ?? "")
            let rank: Int
            if name == query || short == query { rank = 0 }
            else if query.isEmpty || name.hasPrefix(query) || short.hasPrefix(query) { rank = 1 }
            else if name.contains(query) { rank = 2 }
            else if command.description?.lowercased().contains(query) == true { rank = 3 }
            else { return nil }
            return (command, rank, index)
        }.sorted { $0.1 == $1.1 ? $0.2 < $1.2 : $0.1 < $1.1 }.map { $0.0 }
    }
}
