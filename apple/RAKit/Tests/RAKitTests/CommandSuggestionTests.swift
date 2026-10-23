import Testing
@testable import RAKit

@Test func commandSuggestions() {
    let commands = [SlashCommand(name: "compact"), SlashCommand(name: "skill:prototype", description: "Answer a design question")]
    #expect(SlashCommand.suggestions(for: "/", from: commands) == commands)
    #expect(SlashCommand.suggestions(for: "/PROT", from: commands) == [commands[1]])
    #expect(SlashCommand.suggestions(for: "/design", from: commands) == [commands[1]])
    #expect(SlashCommand.suggestions(for: "/", from: commands + commands) == commands)
    #expect(SlashCommand.suggestions(for: "/missing", from: commands).isEmpty)
    for text in ["", "write /prot", " /prot", "/prototype ", "/prototype\n", "/prototype\targs"] {
        #expect(SlashCommand.query(text) == nil)
        #expect(SlashCommand.suggestions(for: text, from: commands).isEmpty)
    }
}

@Test func commandNameRanking() {
    let catalog = [SlashCommand(name: "code-check", description: "Review code"), SlashCommand(name: "preview"), SlashCommand(name: "review-long"), SlashCommand(name: "review")]
    #expect(SlashCommand.suggestions(for: "/review", from: catalog).map(\.name) == ["review", "review-long", "preview", "code-check"])
}
