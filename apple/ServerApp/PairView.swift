import AppKit
import CoreImage.CIFilterBuiltins
import SwiftUI

/// A pairing QR code from `rad pair --print-url`: single use, valid for 10
/// minutes.
struct PairView: View {
    let rad: RadSupervisor
    @State private var link: String?
    @State private var error: String?
    @State private var expires = Date.distantPast
    @State private var lan = false
    @State private var copied = false

    var body: some View {
        VStack(spacing: 14) {
            if let link, let qr = Self.qrCode(link) {
                Image(nsImage: qr)
                    .interpolation(.none)
                    .resizable()
                    .frame(width: 220, height: 220)
                    .padding(12)
                    .background(.white, in: .rect(cornerRadius: 12))
                Text(link)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .multilineTextAlignment(.center)
                    .lineLimit(4)
                    .fixedSize(horizontal: false, vertical: true)
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    Text(expiry(at: context.date))
                        .font(.callout)
                        .foregroundStyle(context.date < expires ? .secondary : Color.red)
                        .monospacedDigit()
                }
                HStack {
                    Button(copied ? "Copied" : "Copy Link") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(link, forType: .string)
                        copied = true
                    }
                    if let url = URL(string: link), NSWorkspace.shared.urlForApplication(toOpen: url) != nil {
                        Button("Open in Remote Agent") { NSWorkspace.shared.open(url) }
                    }
                    Button("New Code") { Task { await newCode() } }
                }
            } else if let error {
                Text(error)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, minHeight: 200)
                Button("Try Again") { Task { await newCode() } }
            } else {
                ProgressView()
                    .frame(width: 244, height: 244)
            }
            Divider()
            Text("In the Remote Agent app on your iPhone or iPad, tap **Pair a server** and scan the code. On a Mac, paste the link instead.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Toggle("Include this Mac's Wi-Fi addresses (plain HTTP)", isOn: $lan)
                .font(.callout)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(20)
        .frame(width: 400)
        .task(id: lan) { await newCode() }
    }

    private func newCode() async {
        copied = false
        do {
            let result = try await RadSupervisor.run(["pair", "--print-url"] + (lan ? ["--lan"] : []), environment: rad.environment)
            let output = result.output.trimmingCharacters(in: .whitespacesAndNewlines)
            // The link is the last line; a note about pair_urls may come first.
            if result.status == 0, let line = output.split(separator: "\n").last, line.hasPrefix("remoteagent://") {
                link = String(line)
                error = nil
                expires = Date().addingTimeInterval(10 * 60)
            } else {
                link = nil
                error = output.isEmpty ? "rad pair failed with status \(result.status)." : output
            }
        } catch {
            link = nil
            self.error = error.localizedDescription
        }
    }

    private func expiry(at now: Date) -> String {
        let left = Int(expires.timeIntervalSince(now))
        guard left > 0 else { return "Expired. Make a new code." }
        return "Single use, expires in \(left / 60):\(String(format: "%02d", left % 60))"
    }

    private static func qrCode(_ text: String) -> NSImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(text.utf8)
        filter.correctionLevel = "M"
        guard let output = filter.outputImage,
              let image = CIContext().createCGImage(output, from: output.extent) else { return nil }
        return NSImage(cgImage: image, size: output.extent.size)
    }
}
