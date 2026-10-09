import AVFoundation
import Observation
import OSLog
import Speech

private let log = Logger(subsystem: "dev.remote-agent.app", category: "dictation")

/// Live speech-to-text for the composer. Like keyboard dictation, it runs on
/// device when the language model is installed and uses Apple's servers otherwise.
@MainActor @Observable
final class Dictation {
    private(set) var isRecording = false
    var error: String?

    private var engine: AVAudioEngine?
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    // Results that arrive after stop() belong to an old run and are dropped.
    private var generation = 0

    /// Starts listening. onText gets the whole transcript so far, each time it changes.
    func start(onText: @escaping @MainActor (String) -> Void) async {
        guard !isRecording else { return }
        guard await Self.authorize() else {
            error = "Allow Microphone and Speech Recognition for Remote Agent in Settings."
            return
        }
        begin(onDevice: true, onText: onText)
    }

    private func begin(onDevice: Bool, onText: @escaping @MainActor (String) -> Void) {
        guard let recognizer = SFSpeechRecognizer(), recognizer.isAvailable else {
            error = "Speech recognition is not available right now."
            return
        }
        let onDevice = onDevice && recognizer.supportsOnDeviceRecognition
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.record, mode: .measurement, options: .duckOthers)
            try session.setActive(true, options: .notifyOthersOnDeactivation)

            let request = SFSpeechAudioBufferRecognitionRequest()
            request.shouldReportPartialResults = true
            request.addsPunctuation = true
            request.requiresOnDeviceRecognition = onDevice
            let engine = AVAudioEngine()
            try Self.installTap(on: engine.inputNode, request: request)
            engine.prepare()
            try engine.start()

            generation += 1
            let gen = generation
            var heard = false
            (self.engine, self.request) = (engine, request)
            task = recognizer.recognitionTask(with: request, resultHandler: Self.resultHandler { [weak self] text, failure in
                guard let self, gen == self.generation else { return }
                if let text {
                    heard = true
                    onText(text)
                }
                guard let failure else { return }
                self.stop()
                if onDevice, !heard, failure.isOnDeviceUnavailable {
                    // The language's on-device model is missing (always so in the Simulator).
                    self.begin(onDevice: false, onText: onText)
                } else if !failure.isExpectedEnd {
                    self.error = failure.message
                }
            })
            isRecording = true
        } catch {
            self.error = error.localizedDescription
            stop()
        }
    }

    func stop() {
        generation += 1
        engine?.stop()
        engine?.inputNode.removeTap(onBus: 0)
        request?.endAudio()
        task?.cancel()
        engine = nil
        request = nil
        task = nil
        if isRecording {
            isRecording = false
            try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        }
    }

    // The audio tap and the recognition callback run on background threads, so
    // they are built outside the main actor (a main-actor closure would trap there).

    private nonisolated static func installTap(on input: AVAudioInputNode, request: SFSpeechAudioBufferRecognitionRequest) throws {
        let format = input.outputFormat(forBus: 0)
        guard format.channelCount > 0, format.sampleRate > 0 else {
            throw DictationError.noMicrophone
        }
        nonisolated(unsafe) let request = request
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in
            request.append(buffer)
        }
    }

    /// Calls deliver with each transcript, then once with the end reason (a
    /// final result ends without a failure; stop() is still needed then).
    private nonisolated static func resultHandler(
        _ deliver: @escaping @MainActor (_ text: String?, _ end: RecognitionEnd?) -> Void
    ) -> @Sendable (SFSpeechRecognitionResult?, (any Error)?) -> Void {
        { result, error in
            let text = result?.bestTranscription.formattedString
            var end: RecognitionEnd?
            if let error = error as NSError? {
                log.notice("recognition ended: \(error.domain, privacy: .public) \(error.code) \(error.localizedDescription, privacy: .public)")
                end = RecognitionEnd(domain: error.domain, code: error.code, message: error.localizedDescription)
            } else if result?.isFinal == true {
                end = RecognitionEnd(domain: "", code: 0, message: "")
            }
            Task { @MainActor in deliver(text, end) }
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

private struct RecognitionEnd: Sendable {
    let domain: String
    let code: Int
    let message: String

    /// Finishing, silence and cancellation are normal ends, not errors.
    var isExpectedEnd: Bool {
        switch (domain, code) {
        case ("", 0),
             ("kAFAssistantErrorDomain", 1110), // no speech detected
             ("kAFAssistantErrorDomain", 216),  // canceled
             ("kLSRErrorDomain", 301):          // canceled
            true
        default:
            false
        }
    }

    /// The on-device recognizer could not start (e.g. its model is not installed).
    var isOnDeviceUnavailable: Bool { domain == "kLSRErrorDomain" && (code == 300 || code == 102) }
}

enum DictationError: LocalizedError {
    case noMicrophone
    var errorDescription: String? { "No microphone is available." }
}
