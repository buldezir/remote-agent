import RAKit
import SwiftUI
#if os(iOS)
import VisionKit
#endif

struct AddServerView: View {
    @Environment(ServerStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    var initialLink: PairingLink?
    var onPaired: (SavedServer) -> Void

    @State private var linkText = ""
    @State private var deviceName = Platform.deviceName
    @State private var scanning = false
    @State private var pairing = false
    @State private var error: String?

    private var link: PairingLink? { PairingLink(string: linkText) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    #if os(iOS)
                    if DataScannerViewController.isSupported {
                        Button {
                            scanning = true
                        } label: {
                            Label("Scan QR code", systemImage: "qrcode.viewfinder")
                        }
                    }
                    #endif
                    HStack {
                        TextField("Pairing link", text: $linkText, prompt: Text("remoteagent://pair?…"), axis: .vertical)
                            .labelsHidden()
                            .font(.footnote.monospaced())
                            .plainTextInput()
                            .lineLimit(1...4)
                        Button("Paste", systemImage: "doc.on.clipboard") {
                            linkText = Platform.pasteboardString ?? ""
                        }
                        .labelStyle(.iconOnly)
                    }
                } header: {
                    Text("Pairing link")
                } footer: {
                    Text(footer)
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
            .inlineTitle()
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
            #if os(iOS)
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
            #else
            .frame(minWidth: 460, minHeight: 420)
            #endif
            .onAppear {
                if let initialLink, linkText.isEmpty {
                    linkText = rebuild(initialLink)
                }
            }
        }
    }

    private var footer: LocalizedStringKey {
        #if os(iOS)
        "On your computer run `rad pair`. The code is valid for 10 minutes and works once."
        #else
        "On the computer with rad, run `rad pair` and paste the link it prints. The code is valid for 10 minutes and works once."
        #endif
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
