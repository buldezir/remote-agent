import AVFAudio
import OSLog
import Speech

private let log = Logger(subsystem: "dev.remote-agent.app", category: "dictation")

/// Transcribes live audio in a few languages at once with the on-device
/// dictation models, and reports the transcript of the language that fits best.
///
/// Only final results carry a confidence, and they mostly arrive after the
/// audio ends. So the first locale, the preferred language, is shown until
/// every language has final text, and finish() settles the choice.
@MainActor
final class LiveTranscription {
    /// Takes the microphone's buffers.
    let feed: AudioFeed
    private let analyzer: SpeechAnalyzer
    private var lanes: [Lane]
    private var readers: [Task<Void, Never>] = []
    private var onText: (@MainActor (String) -> Void)?
    private var onFailure: (@MainActor (any Error) -> Void)?
    private var ended = false
    private var shown = 0
    private var lastText = ""

    /// The locales in wanted whose models are on the device, in order.
    static func usable(_ wanted: [Locale]) async -> [Locale] {
        var out: [Locale] = []
        for locale in wanted {
            // There is no audio format until the model is installed. (Asset
            // status says "supported" even for the models keyboard dictation installed.)
            if await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber(locale)]) != nil {
                out.append(locale)
            }
        }
        return out
    }

    /// Downloads the model for locale. Returns false if there is nothing to download.
    static func install(_ locale: Locale) async throws -> Bool {
        guard let request = try await AssetInventory.assetInstallationRequest(supporting: [transcriber(locale)]) else {
            return false
        }
        try await request.downloadAndInstall()
        return true
    }

    // DictationTranscriber has keyboard dictation's languages. SpeechTranscriber,
    // the newer model, has fewer: in iOS 27 its 45 locales leave out Russian.
    private static func transcriber(_ locale: Locale) -> DictationTranscriber {
        DictationTranscriber(locale: locale, contentHints: [], transcriptionOptions: [.punctuation],
                             reportingOptions: [.volatileResults], attributeOptions: [.transcriptionConfidence])
    }

    /// Starts transcribing in locales, which must be usable. onText gets the
    /// whole transcript so far each time it changes. hints are words to expect.
    init(locales: [Locale], hints: [String],
         onText: @escaping @MainActor (String) -> Void,
         onFailure: @escaping @MainActor (any Error) -> Void) async throws {
        let transcribers = locales.map(Self.transcriber)
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: transcribers) else {
            throw DictationError.noModel
        }
        let (stream, input) = AsyncStream.makeStream(of: AnalyzerInput.self)
        analyzer = SpeechAnalyzer(modules: transcribers, options: .init(priority: .userInitiated, modelRetention: .lingering))
        feed = AudioFeed(converter: AnalyzerInputConverter(analyzerFormat: format), input: input)
        lanes = locales.map { Lane(locale: $0) }
        self.onText = onText
        self.onFailure = onFailure

        if !hints.isEmpty {
            let context = AnalysisContext()
            context.contextualStrings[.general] = hints
            do {
                try await analyzer.setContext(context)
            } catch {
                log.notice("hints not used: \(error.localizedDescription, privacy: .public)")
            }
        }
        try await analyzer.prepareToAnalyze(in: format)
        try await analyzer.start(inputSequence: stream)
        // No audio arrives before the caller starts the microphone, so no result is missed.
        for (i, transcriber) in transcribers.enumerated() {
            readers.append(Task { [weak self] in
                do {
                    for try await result in transcriber.results {
                        self?.update(lane: i, with: result.text, isFinal: result.isFinal)
                    }
                } catch {
                    log.error("transcription failed: \(error.localizedDescription, privacy: .public)")
                    self?.fail(error)
                }
            })
        }
    }

    /// Ends the audio and waits for the final pass, which corrects the last
    /// words and settles the language. Returns the language picked.
    func finish() async -> Locale {
        feed.close()
        let analyzer = analyzer
        let timeout = Task {
            try await Task.sleep(for: .seconds(3))
            log.notice("final pass timed out")
            await analyzer.cancelAndFinishNow()
        }
        do {
            try await analyzer.finalizeAndFinishThroughEndOfInput()
        } catch {
            log.error("final pass failed: \(error.localizedDescription, privacy: .public)")
        }
        for reader in readers {
            await reader.value
        }
        timeout.cancel()
        ended = true
        publish()
        let scores = lanes.map { "\($0.locale.identifier)=\($0.confidence.map { String(format: "%.2f", $0) } ?? "-")" }
        log.info("picked \(self.lanes[self.shown].locale.identifier, privacy: .public) from \(scores.joined(separator: " "), privacy: .public)")
        onText = nil
        onFailure = nil
        return lanes[shown].locale
    }

    /// Drops the audio and any results still to come.
    func cancel() async {
        onText = nil
        onFailure = nil
        feed.close()
        for reader in readers {
            reader.cancel()
        }
        await analyzer.cancelAndFinishNow()
    }

    private func update(lane i: Int, with text: AttributedString, isFinal: Bool) {
        if isFinal {
            lanes[i].final += text
            lanes[i].volatile = AttributedString()
        } else {
            lanes[i].volatile = text
        }
        publish()
    }

    private func fail(_ error: any Error) {
        let onFailure = onFailure
        self.onFailure = nil
        onFailure?(error)
    }

    private func publish() {
        shown = pick()
        let text = lanes[shown].text
        guard text != lastText else { return }
        lastText = text
        onText?(text)
    }

    /// The lane to show: the first until every lane has final text, then the
    /// most confident. At the end, a lane without final text heard nothing.
    private func pick() -> Int {
        let scores = lanes.map(\.confidence)
        if !ended, scores.contains(where: { $0 == nil }) {
            return 0
        }
        var best = 0
        for i in scores.indices where (scores[i] ?? -1) > (scores[best] ?? -1) {
            best = i
        }
        return best
    }

    private struct Lane {
        let locale: Locale
        var final = AttributedString()
        var volatile = AttributedString()

        var text: String {
            String((final + volatile).characters).trimmingCharacters(in: .whitespacesAndNewlines)
        }

        /// The mean confidence of the final text's words, or nil before there is any.
        var confidence: Double? {
            var sum = 0.0
            var n = 0
            for run in final.runs {
                if let c = run[AttributeScopes.SpeechAttributes.ConfidenceAttribute.self] {
                    sum += c
                    n += 1
                }
            }
            return n > 0 ? sum / Double(n) : nil
        }
    }
}

/// Hands microphone buffers to the analyzer in its audio format. append() runs
/// on the audio thread, one buffer at a time; close() runs once the tap is removed.
final class AudioFeed: @unchecked Sendable {
    private let converter: AnalyzerInputConverter
    private let input: AsyncStream<AnalyzerInput>.Continuation
    private var closed = false

    init(converter: AnalyzerInputConverter, input: AsyncStream<AnalyzerInput>.Continuation) {
        self.converter = converter
        self.input = input
    }

    func append(_ buffer: AVAudioPCMBuffer) {
        do {
            for chunk in try converter.convert(buffer, at: nil) {
                input.yield(chunk)
            }
        } catch {
            log.error("audio conversion failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    func close() {
        guard !closed else { return }
        closed = true
        for chunk in (try? converter.flush()) ?? [] {
            input.yield(chunk)
        }
        input.finish()
    }
}

enum DictationError: LocalizedError {
    case notAllowed, noMicrophone, noLanguage, noModel

    var errorDescription: String? {
        switch self {
        case .notAllowed: "Allow Microphone and Speech Recognition for Remote Agent in Settings."
        case .noMicrophone: "No microphone is available."
        case .noLanguage: "Dictation doesn't support any of your keyboard languages."
        case .noModel: "The speech model for your language isn't available on this device."
        }
    }
}
