import SwiftUI

/// The hold switch, as a pill of Liquid Glass floating beside the tabs (BottomBar), the same size
/// as each tab. Its symbol is what pressing does, as in a media player: pause while taking calls,
/// play while on hold; its word is the state, "Taking calls" or "On hold". On hold it's indigo,
/// apart from the red of missed calls and the orange of waiting ones, with how many calls wait.
/// Control Center has the same switch (HoldControl).
struct HoldButton: View {
    let state: AppState
    @State private var isSetting = false

    private var on: Bool { state.hold?.on == true }

    var body: some View {
        Button(action: toggle) {
            VStack(spacing: 2) {
                Image(systemName: on ? "play.fill" : "pause.fill")
                    .font(.system(size: 18, weight: .semibold))
                Text(word).font(.caption2.weight(.medium)).lineLimit(1)
            }
            .foregroundStyle(on ? .white : .primary)
            .frame(width: BottomBar.pillWidth, height: BottomBar.pillHeight)
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .glassEffect(on ? .regular.tint(.indigo).interactive() : .regular.interactive(), in: .capsule)
        .overlay(alignment: .topTrailing) {
            if on && !state.waiting.isEmpty {
                Text("\(state.waiting.count)")
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(.white)
                    .frame(minWidth: 20, minHeight: 20)
                    .background(.orange, in: Capsule())
                    .offset(x: 4, y: -4)
                    .accessibilityHidden(true)
            }
        }
        .opacity(state.hold == nil ? 0.5 : 1)
        .disabled(state.hold == nil || isSetting)
        .accessibilityLabel(on ? "Take calls" : "Hold calls")
        .accessibilityValue(detail)
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

    private var word: String {
        guard let hold = state.hold else { return "Hold" }
        return hold.on ? "On hold" : "Taking calls"
    }

    /// For VoiceOver: "Taking calls, holds calls at 19:00", or "On hold, 2 waiting, takes
    /// calls tomorrow at 08:15".
    private var detail: String {
        guard let hold = state.hold else { return "Shows once the switchboard answers" }
        guard hold.on else { return "Taking calls, " + (hold.until.map { "holds calls \($0.ringsLabel)" } ?? "until you put calls on hold") }
        let count = state.waiting.count
        let waiting = count == 0 ? "nothing waiting" : "\(count) waiting"
        return "On hold, \(waiting), " + (hold.until.map { "takes calls \($0.ringsLabel)" } ?? "until you take calls")
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

/// The app's tabs and the hold switch, as pills in Liquid Glass, after your sketch: Recents and
/// Sessions in one capsule, and the switch floating beside it at the same size. It takes the
/// place of the system tab bar (hidden), whose only button of its own beside the tabs is round,
/// and for Search.
struct BottomBar: View {
    @Binding var page: RootView.Page
    let state: AppState

    static let pillWidth: CGFloat = 100
    static let pillHeight: CGFloat = 52

    var body: some View {
        GlassEffectContainer(spacing: 10) {
            HStack(spacing: 10) {
                HStack(spacing: 4) {
                    tab(.recents, "Recents", "clock.fill")
                    tab(.sessions, "Sessions", "terminal.fill")
                }
                .padding(4)
                .glassEffect(.regular, in: .capsule)
                HoldButton(state: state)
            }
        }
        .padding(.bottom, 2)
    }

    private func tab(_ value: RootView.Page, _ title: String, _ symbol: String) -> some View {
        let selected = page == value
        return Button {
            page = value
        } label: {
            VStack(spacing: 2) {
                Image(systemName: symbol).font(.system(size: 18, weight: .medium))
                Text(title).font(.caption2.weight(.medium))
            }
            .foregroundStyle(selected ? Color.accentColor : .primary)
            .frame(width: Self.pillWidth, height: Self.pillHeight)
            .background {
                if selected { Capsule().fill(.quaternary) }
            }
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? [.isSelected, .isButton] : .isButton)
    }
}
