import SwiftUI

/// The call, over whatever you're looking at ("Using the app during a call" in docs/design.md):
/// its screen while it shows, and once you've minimised it, a green pill at the top left with
/// the call's time, like iOS's own, which brings it back. SwiftUI presents one screen at a time
/// from each view, so the app's tabs and a shown item's viewer each carry this, and the one on
/// top presents the call screen (isTop).
struct CallOnTop: ViewModifier {
    let state: AppState
    let calls: CallService
    /// Whether nothing else is presented over this view.
    var isTop = true
    /// Once the call screen has gone: the call ended, or you minimised it.
    var gone: () -> Void = {}

    func body(content: Content) -> some View {
        let pill = state.callMinimised ? state.activeCall : nil
        // The pill gets a row of its own above the screen, navigation bar and all: iOS 26's
        // bars float over a safe-area inset, so the pill would cover their buttons.
        content
            .padding(.top, pill == nil ? 0 : CallPill.row)
            .overlay(alignment: .topLeading) {
                if let pill {
                    CallPill(call: pill) { state.callMinimised = false }
                        .padding(.leading, 16)
                        .padding(.top, 2)
                }
            }
            .fullScreenCover(
                isPresented: Binding(
                    get: { isTop && state.activeCall != nil && !state.callMinimised },
                    set: { if !$0 && state.activeCall != nil { state.callMinimised = true } }),
                onDismiss: {
                    gone()
                    state.callScreenGone()
                }
            ) {
                InCallView(state: state, calls: calls)
            }
    }
}

/// The green pill of a minimised call: a phone and the call's time; tapping it brings the call
/// screen back.
struct CallPill: View {
    let call: ActiveCall
    let action: () -> Void

    /// The height of its row.
    static let row: CGFloat = 38

    var body: some View {
        Button(action: action) {
            HStack(spacing: 5) {
                Image(systemName: "phone.fill").font(.caption.weight(.bold))
                if case .connected = call.audio {
                    TimelineView(.periodic(from: call.answeredAt, by: 1)) { context in
                        Text(Duration.seconds(context.date.timeIntervalSince(call.answeredAt)),
                             format: .time(pattern: .minuteSecond))
                            .monospacedDigit()
                    }
                } else {
                    Text("Call")
                }
            }
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(Color.green, in: Capsule())
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Return to the call with \(call.callerName)")
        .accessibilityIdentifier("Return to the call")
    }
}
