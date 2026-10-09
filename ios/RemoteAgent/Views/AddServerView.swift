import RAKit
import SwiftUI
import VisionKit

struct AddServerView: View {
    @Environment(ServerStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    var initialLink: PairingLink?
    var onPaired: (SavedServer) -> Void

    @State private var linkText = ""
    @State private var deviceName = UIDevice.current.name
    @State private var scanning = false
    @State private var pairing = false
    @State private var error: String?

    private var link: PairingLink? { PairingLink(string: linkText) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    if DataScannerViewController.isSupported {
                        Button {
                            scanning = true
                        } label: {
                            Label("Scan QR code", systemImage: "qrcode.viewfinder")
                        }
                    }
                    HStack {
                        TextField("remoteagent://pair?…", text: $linkText, axis: .vertical)
                            .font(.footnote.monospaced())
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .lineLimit(1...4)
                        Button("Paste", systemImage: "doc.on.clipboard") {
                            linkText = UIPasteboard.general.string ?? ""
                        }
                        .labelStyle(.iconOnly)
                    }
                } header: {
                    Text("Pairing link")
                } footer: {
                    Text("On your computer run `rad pair`. The code is valid for 10 minutes and works once.")
                }

                if let link {
                    Section("Server") {
                        LabeledContent("Name", value: link.name)
                        ForEach(link.urls, id: \.self) { url in
                            Text(url.absoluteString).font(.footnote.monospaced()).foregroundStyle(.secondary)
                        }
                    }
                }

                Section("This device") {
                    TextField("Device name", text: $deviceName)
                }

                if let error {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                    }
                }
            }
            .compactForm()
            .navigationTitle("Pair a server")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if pairing {
                        ProgressView()
                    } else {
                        Button("Pair") { Task { await pair() } }
                            .disabled(link == nil)
                    }
                }
            }
            .fullScreenCover(isPresented: $scanning) {
                QRScannerView { payload in
                    if PairingLink(string: payload) != nil {
                        linkText = payload
                        scanning = false
                        Task { await pair() }
                    }
                } onCancel: {
                    scanning = false
                }
                .ignoresSafeArea()
            }
            .onAppear {
                if let initialLink, linkText.isEmpty {
                    linkText = rebuild(initialLink)
                }
            }
        }
    }

    private func rebuild(_ l: PairingLink) -> String {
        var c = URLComponents()
        c.scheme = "remoteagent"
        c.host = "pair"
        c.queryItems = [.init(name: "name", value: l.name), .init(name: "code", value: l.code)] + l.urls.map { .init(name: "url", value: $0.absoluteString) }
        return c.string ?? ""
    }

    private func pair() async {
        guard let link, !pairing else { return }
        pairing = true
        error = nil
        defer { pairing = false }
        do {
            let server = try await store.pair(link, deviceName: deviceName)
            dismiss()
            onPaired(server)
        } catch {
            self.error = error.localizedDescription
        }
    }
}

/// Wraps VisionKit's live QR scanner.
struct QRScannerView: UIViewControllerRepresentable {
    var onFound: (String) -> Void
    var onCancel: () -> Void

    func makeUIViewController(context: Context) -> UINavigationController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced, isHighlightingEnabled: true)
        scanner.delegate = context.coordinator
        scanner.navigationItem.leftBarButtonItem = UIBarButtonItem(
            systemItem: .cancel, primaryAction: UIAction { _ in onCancel() })
        scanner.title = "Scan pairing code"
        try? scanner.startScanning()
        return UINavigationController(rootViewController: scanner)
    }

    func updateUIViewController(_ vc: UINavigationController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(onFound: onFound) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let onFound: (String) -> Void
        init(onFound: @escaping (String) -> Void) { self.onFound = onFound }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for item in items {
                if case .barcode(let code) = item, let s = code.payloadStringValue {
                    onFound(s)
                }
            }
        }
    }
}
