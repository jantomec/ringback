import SwiftUI

/// The hold switch, as a pill of Liquid Glass floating beside the tabs (BottomBar), as wide as a
/// tab and as tall as the tabs' capsule. Its symbol is what pressing does, as in a media player:
/// pause while taking calls, play while on hold; its word is the state, "Taking calls" or "On
/// hold". On hold it's indigo, apart from the red of missed calls and the orange of waiting ones,
/// with how many calls wait, and it breathes: its glow swells and fades (Breath), and the app
/// behind takes up a faint share of that light (BreathWash). Control Center has the same switch
/// (HoldControl).
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
            .frame(width: BottomBar.pillWidth, height: BottomBar.pillHeight + 2 * BottomBar.inset)
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .glassEffect(on ? .regular.tint(.indigo).interactive() : .regular.interactive(), in: .capsule)
        .background {
            if on { BreathGlow() }
        }
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
        guard let hold = state.hold else { return "Shows once the hub answers" }
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
                state.callBackProblem = "Can't reach your hub to \(on ? "hold" : "take") calls. If you reach it over Nebula or another VPN, check that it's on."
            }
            isSetting = false
            await state.refreshWaiting()
        }
    }
}

/// The app's tabs and the hold switch, as pills in Liquid Glass, after your sketch: Recents and
/// Sessions in one capsule, and the switch floating beside it, as tall as the capsule. It takes the
/// place of the system tab bar (hidden), whose only button of its own beside the tabs is round,
/// and for Search.
struct BottomBar: View {
    @Binding var page: RootView.Page
    let state: AppState

    static let pillWidth: CGFloat = 100
    static let pillHeight: CGFloat = 52
    /// Between the tabs and the capsule's edge.
    static let inset: CGFloat = 4
    /// Between the capsule and the switch.
    static let spacing: CGFloat = 10

    var body: some View {
        GlassEffectContainer(spacing: Self.spacing) {
            HStack(spacing: Self.spacing) {
                HStack(spacing: Self.inset) {
                    tab(.recents, "Recents", "clock.fill")
                    tab(.sessions, "Sessions", "terminal.fill")
                }
                .padding(Self.inset)
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

/// The hold's breathing light: one clock for the pill's glow and the app's wash, so they swell and
/// fade together, in phase, about 13 times a minute, as the sleep light of Apple's laptops did:
/// resting, not off. Still, at a middle level, with Reduce Motion or in Low Power Mode.
enum Breath {
    static let period = 4.6  // seconds
    static let still = 0.6

    /// From 0 to 1 and back, at `date`, the same for every view that asks.
    static func level(at date: Date) -> Double {
        0.5 - 0.5 * cos(2 * .pi * date.timeIntervalSinceReferenceDate / period)
    }

    static func moves(reduceMotion: Bool) -> Bool {
        !reduceMotion && !ProcessInfo.processInfo.isLowPowerModeEnabled
    }
}

/// The light the pill gives off on hold: an indigo halo behind its glass, swelling and fading.
struct BreathGlow: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let moving = Breath.moves(reduceMotion: reduceMotion)
        TimelineView(.animation(minimumInterval: 1.0 / 30, paused: !moving)) { context in
            let level = moving ? Breath.level(at: context.date) : Breath.still
            Capsule()
                .fill(Color.indigo)
                .scaleEffect(1 + 0.14 * level)
                .blur(radius: 4 + 12 * level)
                .opacity(0.25 + 0.65 * level)
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

/// What reaches the rest of the app of the pill's light on hold: a faint indigo wash, strongest
/// near the pill and fading with distance, breathing with it, a small share of its intensity.
/// Over the content, under the bar; light on dark backgrounds, a tint on light ones.
struct BreathWash: View {
    let on: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        GeometryReader { geometry in
            if on {
                let moving = Breath.moves(reduceMotion: reduceMotion)
                let size = geometry.size
                // The pill's centre: the bar is centred, and the pill is its last part; its
                // middle is about 66 pt above the screen's bottom edge.
                let bar = 2 * BottomBar.pillWidth + 3 * BottomBar.inset + BottomBar.spacing + BottomBar.pillWidth
                let source = UnitPoint(
                    x: ((size.width + bar) / 2 - BottomBar.pillWidth / 2) / max(size.width, 1),
                    y: 1 - 66 / max(size.height, 1))
                TimelineView(.animation(minimumInterval: 1.0 / 30, paused: !moving)) { context in
                    let level = moving ? Breath.level(at: context.date) : Breath.still
                    RadialGradient(
                        colors: [.indigo.opacity(0.18 * level), .indigo.opacity(0.07 * level), .indigo.opacity(0.035 * level)],
                        center: source, startRadius: 0, endRadius: size.height)
                        .blendMode(scheme == .dark ? .plusLighter : .normal)
                }
                .transition(.opacity)
            }
        }
        .ignoresSafeArea()
        .allowsHitTesting(false)
        .accessibilityHidden(true)
        .animation(.easeInOut(duration: 0.6), value: on)
    }
}
