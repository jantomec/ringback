import AppIntents
import SwiftUI
import WidgetKit

/// Ringback's extension: the hold switch in Control Center, and on the Lock Screen.
@main
struct RingbackControls: WidgetBundle {
    var body: some Widget {
        HoldControl()
        HoldWidget()
    }
}

/// The hold switch in Control Center, which you can also put on the Lock Screen or the Action
/// button. On (indigo) holds every call; the symbol is what pressing does, as in the app:
/// pause while taking calls, play while on hold. It shows the switch as the switchboard has it,
/// or as last heard if it can't be reached.
struct HoldControl: ControlWidget {
    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: HoldSwitch.controlKind, provider: Provider()) { onHold in
            ControlWidgetToggle("Ringback", isOn: onHold, action: SetHoldIntent()) { onHold in
                Label(onHold ? "Calls on hold" : "Taking calls", systemImage: onHold ? "play.fill" : "pause.fill")
            }
            .tint(.indigo)
        }
        .displayName("Hold Calls")
        .description("Holds every call from your Claude Code sessions until you take calls.")
    }

    struct Provider: ControlValueProvider {
        var previewValue: Bool { false }

        func currentValue() async throws -> Bool {
            if let state = try? await HoldSwitch.fetch() { return state.on }
            return HoldSwitch.cached?.on ?? false
        }
    }
}
