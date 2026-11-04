import AppKit
import MetaRushCore
import SwiftUI

/// FinishingModel shows the steps after an update (D472) as they run.
@MainActor
final class FinishingModel: ObservableObject {
    @Published var states: [FinishStep: StepState] = [:]
    @Published var failure: FinishFailure?
    @Published var finished = false
    @Published var running = false

    let metarush: MetaRush
    /// The version the app was updated to (what the steps finish).
    let version: String
    let onFinished: () -> Void
    let showLogs: () -> Void

    init(metarush: MetaRush, version: String, onFinished: @escaping () -> Void, showLogs: @escaping () -> Void) {
        self.metarush = metarush
        self.version = version
        self.onFinished = onFinished
        self.showLogs = showLogs
    }

    func run() {
        guard !running else { return }
        failure = nil
        finished = false
        running = true
        states = Dictionary(uniqueKeysWithValues: FinishStep.allCases.map { ($0, StepState.waiting) })
        Task {
            let result = await Finishing(metarush: metarush).run { step, state in
                await MainActor.run { self.states[step] = state }
            }
            running = false
            switch result {
            case .success:
                finished = true
                onFinished()
            case .failure(let f):
                failure = f
            }
        }
    }

    /// useThisCopy lets the app's copy run in the background, then finishes.
    func useThisCopy() {
        Task {
            do {
                try await metarush.call(["service", "install"])
                run()
            } catch {
                failure = FinishFailure(step: nil, error: plainError(error))
            }
        }
    }
}

struct FinishingView: View {
    @ObservedObject var model: FinishingModel
    let close: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(Words.finishingHeading(model.version)).font(.title3.bold())
            Plain(Words.finishingIntro, secondary: true)
            ForEach(FinishStep.allCases, id: \.self) { step in
                row(step, model.states[step] ?? .waiting)
            }
            if let f = model.failure {
                ErrorBox(error: f.error)
                if f.otherCopy {
                    HStack {
                        Button(Words.notNow, action: close)
                        Spacer()
                        Button(Words.useThisCopy) { model.useThisCopy() }.keyboardShortcut(.defaultAction)
                    }
                } else {
                    HStack {
                        Button(Words.close, action: close)
                        Button(Words.showLogs) { model.showLogs() }
                        Spacer()
                        Button(Words.tryAgain) { model.run() }.keyboardShortcut(.defaultAction)
                    }
                }
            } else if model.finished {
                Label { Text(Words.finished(model.version)).bold() } icon: { Image(systemName: "checkmark.seal.fill").foregroundStyle(.green) }
                HStack {
                    Spacer()
                    Button(Words.done, action: close).keyboardShortcut(.defaultAction)
                }
            }
        }
        .padding(22)
    }

    private func row(_ step: FinishStep, _ state: StepState) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Group {
                switch state {
                case .waiting: Image(systemName: "circle").foregroundStyle(.secondary)
                case .running: ProgressView().controlSize(.small)
                case .done: Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                case .skipped: Image(systemName: "minus.circle").foregroundStyle(.secondary)
                case .failed: Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
                }
            }
            .frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(Words.stepTitle(step, version: model.version))
                switch state {
                case .done(let says?) where !says.isEmpty: Plain(says, secondary: true).font(.caption)
                case .skipped(let why): Plain(why, secondary: true).font(.caption)
                default: EmptyView()
                }
            }
        }
    }
}
