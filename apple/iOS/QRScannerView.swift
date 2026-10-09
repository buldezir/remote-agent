import SwiftUI
import VisionKit

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
