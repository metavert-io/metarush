import AppKit
import MetaRushCore
import SwiftUI

/// Window shows one SwiftUI view in a plain window brought to the front (the
/// app has no Dock icon, so it activates itself first).
@MainActor
final class Window {
    private var window: NSWindow?

    func show<V: View>(_ title: String, width: CGFloat = 560, _ view: V) {
        let host = NSHostingController(rootView: view.frame(width: width).fixedSize(horizontal: false, vertical: true))
        if let window {
            window.contentViewController = host
            window.title = title
        } else {
            let w = NSWindow(contentViewController: host)
            w.styleMask = [.titled, .closable]
            w.title = title
            w.isReleasedWhenClosed = false
            w.center()
            window = w
        }
        NSApp.activate(ignoringOtherApps: true)
        window?.makeKeyAndOrderFront(nil)
    }

    func close() {
        window?.close()
        window = nil
    }

    var visible: Bool { window?.isVisible ?? false }
}

/// Plain is a paragraph of the app's words, wrapping.
struct Plain: View {
    let text: String
    var secondary = false

    init(_ text: String, secondary: Bool = false) {
        self.text = text
        self.secondary = secondary
    }

    var body: some View {
        Text(text)
            .foregroundStyle(secondary ? .secondary : .primary)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// ErrorBox says what went wrong in plain words, with the technical part
/// behind Details.
struct ErrorBox: View {
    let error: MetaRushError

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label {
                Text(error.message).bold().fixedSize(horizontal: false, vertical: true)
            } icon: {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
            }
            if let hint = error.hint, !hint.isEmpty { Plain(hint, secondary: true) }
            if !error.details.isEmpty {
                DisclosureGroup(Words.details) {
                    Text(error.details)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .padding(10)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.orange.opacity(0.08)))
    }
}

/// The app's own MetaRush errors from anything thrown.
func plainError(_ error: Error) -> MetaRushError {
    (error as? MetaRushError) ?? MetaRushError(code: "", message: Words.failedWithoutSaying, detail: "\(error)")
}
