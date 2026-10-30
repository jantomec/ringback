import SwiftUI

/// Rows like the app's waiting calls, in a Form, each a Button with a trailing swipe action
/// as the app has it: one enabled, one with `.disabled(true)`, and one tinted grey that stays
/// enabled, which is what the app did while Remote Control was off (its tap explained, in an
/// alert there). The text at the top says which action ran last, or which row was tapped.
@main
struct SwipeCheckApp: App {
    var body: some Scene {
        WindowGroup { RowsView() }
    }
}

struct RowsView: View {
    @State private var ran = "nothing"

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Text("ran: \(ran)").accessibilityIdentifier("ran")
                }
                Section("Calls") {
                    row("Enabled row", isOn: true)
                    row("Disabled row", isOn: false)
                    row("Greyed row", isOn: true, tint: .gray)
                }
            }
            .navigationTitle("Swipe check")
        }
    }

    private func row(_ name: String, isOn: Bool, tint: Color = .indigo) -> some View {
        Button { ran = "tapped \(name)" } label: {
            HStack {
                Image(systemName: "clock.fill").foregroundStyle(.orange)
                Text(name)
                Spacer()
            }
            .foregroundStyle(.primary)
        }
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            Button { ran = "action of \(name)" } label: {
                Label("Claude App", systemImage: "arrow.up.forward.app")
            }
            .tint(tint)
            .disabled(!isOn)
        }
    }
}
