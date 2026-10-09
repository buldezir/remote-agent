import Foundation

public enum WireCoding {
    /// Go marshals times as RFC 3339 with up to nanosecond precision.
    public static func parseDate(_ s: String) -> Date? {
        var str = s
        // Trim fractional seconds to milliseconds, which ISO8601 parsing handles.
        if let dot = str.firstIndex(of: ".") {
            var end = str.index(after: dot)
            while end < str.endIndex, str[end].isNumber { end = str.index(after: end) }
            let digits = str[str.index(after: dot)..<end]
            let ms = String(digits.prefix(3)).padding(toLength: 3, withPad: "0", startingAt: 0)
            str.replaceSubrange(dot..<end, with: "." + ms)
            return fractional.date(from: str)
        }
        return plain.date(from: str)
    }

    nonisolated(unsafe) private static let fractional: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    nonisolated(unsafe) private static let plain: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    public static func decoder() -> JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            guard let date = parseDate(s) else {
                throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "bad date \(s)"))
            }
            return date
        }
        return d
    }

    public static func encoder() -> JSONEncoder {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .iso8601
        return e
    }
}
