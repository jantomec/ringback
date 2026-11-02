import SwiftUI

/// The hold switch, in the bar above the tabs (the tab bar's bottom accessory, where Music
/// shows what's playing), so it's on both tabs and under your thumb. It says the state in
/// words, what happens next on the second line, and its button is what pressing does, as in a
/// media player: pause while taking calls, play while on hold. On hold is indigo, apart from the
/// red of missed calls and the orange of waiting ones. When the tab bar folds as you scroll, the
/// bar folds beside it to one line. Control Center has the same switch (HoldControl).
struct HoldBar: View {
    let state: AppState
    @Environment(\.tabViewBottomAccessoryPlacement) private var placement
    @State private var isSetting = false

    var body: some View {
        HStack(spacing: 10) {
            mark
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.subheadline.weight(.semibold)).lineLimit(1)
                if placement != .inline {
                    Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .accessibilityElement(children: .combine)
            Spacer(minLength: 4)
            Button(action: toggle) {
                Image(systemName: state.hold?.on == true ? "play.fill" : "pause.fill")
                    .font(.body.weight(.semibold))
                    .frame(width: 36, height: 36)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(state.hold == nil || isSetting)
            .accessibilityLabel(state.hold?.on == true ? "Take calls" : "Hold calls")
        }
        .padding(.leading, 14)
        .padding(.trailing, 6)
        .sensoryFeedback(.selection, trigger: state.hold?.on)
        // The work hours change it by themselves: show that when they do, then ask.
        .task(id: state.hold?.until) {
            guard let until = state.hold?.until else { return }
            try? await Task.sleep(for: .seconds(max(1, until.timeIntervalSinceNow + 1)))
            guard !Task.isCancelled else { return }
            state.hold = state.hold?.at(.now)
            await state.refreshWaiting()
        }
    }

    @ViewBuilder private var mark: some View {
        if state.hold?.on == true {
            Image(systemName: "pause.fill")
                .font(.caption2.weight(.bold))
                .foregroundStyle(.white)
                .frame(width: 26, height: 26)
                .background(.indigo, in: RoundedRectangle(cornerRadius: 7))
        } else {
            Circle()
                .fill(state.hold == nil ? Color.secondary : .green)
                .frame(width: 9, height: 9)
                .frame(width: 26, height: 26)
                .background((state.hold == nil ? Color.secondary : .green).opacity(0.18), in: RoundedRectangle(cornerRadius: 7))
        }
    }

    private var title: String {
        guard let hold = state.hold else { return "Hold switch" }
        return hold.on ? "Calls on hold" : "Taking calls"
    }

    /// "Holds calls at 19:00", or "2 waiting · takes calls tomorrow at 08:15".
    private var detail: String {
        guard let hold = state.hold else { return "Shows once the switchboard answers" }
        guard hold.on else { return hold.until.map { "Holds calls \($0.ringsLabel)" } ?? "Until you put calls on hold" }
        let count = state.waiting.count
        let waiting = count == 0 ? "Nothing waiting" : "\(count) waiting"
        return "\(waiting) · " + (hold.until.map { "takes calls \($0.ringsLabel)" } ?? "until you take calls")
    }

    /// Shows the new state at once, then sets it at the switchboard; taking calls, the waiting
    /// calls' Macs ask again now, so they ring. If the switchboard can't be reached, it goes back
    /// and says so.
    private func toggle() {
        guard let before = state.hold else { return }
        let on = !before.on
        isSetting = true
        state.hold = HoldState(on: on, until: nil)
        Task { @MainActor in
            do {
                state.hold = try await HoldSwitch.set(on)
            } catch {
                state.hold = before
                state.callBackProblem = "Can't reach the switchboard to \(on ? "hold" : "take") calls. Check that Nebula is on."
            }
            isSetting = false
            await state.refreshWaiting()
        }
    }
}
