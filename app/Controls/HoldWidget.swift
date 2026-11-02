import SwiftUI
import WidgetKit

/// The hold switch on the Lock Screen: whether calls are on hold, how many wait, and until when,
/// so you see it before you open Ringback. It asks the switchboard when iOS refreshes it (or the
/// app tells it something changed), and has a second entry ready for when the work hours change
/// the switch by themselves.
struct HoldWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: HoldSwitch.widgetKind, provider: Provider()) { entry in
            HoldWidgetView(entry: entry)
                .containerBackground(for: .widget) { Color.clear }
        }
        .configurationDisplayName("Hold")
        .description("Whether Ringback holds your calls, and how many wait.")
        .supportedFamilies([.accessoryRectangular, .accessoryCircular, .accessoryInline])
    }

    struct Entry: TimelineEntry {
        let date: Date
        let hold: HoldState?
        let waiting: Int
    }

    struct Provider: TimelineProvider {
        func placeholder(in context: Context) -> Entry {
            Entry(date: .now, hold: HoldState(on: true, until: nil), waiting: 2)
        }

        func getSnapshot(in context: Context, completion: @escaping (Entry) -> Void) {
            if context.isPreview { return completion(placeholder(in: context)) }
            Task { completion(await now()) }
        }

        func getTimeline(in context: Context, completion: @escaping (Timeline<Entry>) -> Void) {
            Task {
                let now = await now()
                var entries = [now]
                // When the hours change the switch: taking calls, the waiting calls ring.
                if let until = now.hold?.until, let hold = now.hold {
                    entries.append(Entry(date: until, hold: hold.at(until), waiting: hold.on ? 0 : now.waiting))
                }
                completion(Timeline(entries: entries, policy: .after(min(now.hold?.until ?? .distantFuture, .now + 1800))))
            }
        }

        private func now() async -> Entry {
            let (hold, waiting) = await HoldSwitch.status()
            return Entry(date: .now, hold: hold, waiting: waiting)
        }
    }
}

private struct HoldWidgetView: View {
    let entry: HoldWidget.Entry
    @Environment(\.widgetFamily) private var family

    private var on: Bool { entry.hold?.on == true }

    var body: some View {
        switch family {
        case .accessoryCircular:
            ZStack {
                AccessoryWidgetBackground()
                VStack(spacing: 0) {
                    Image(systemName: on ? "pause.fill" : "phone.fill").font(.system(size: 15, weight: .semibold))
                    if on { Text("\(entry.waiting)").font(.system(size: 13, weight: .semibold)) }
                }
            }
            .widgetAccentable()
        case .accessoryInline:
            Label(on ? "On hold · \(entry.waiting) waiting" : "Taking calls", systemImage: on ? "pause.fill" : "phone.fill")
        default:
            HStack(spacing: 8) {
                Image(systemName: on ? "pause.circle.fill" : "phone.circle.fill")
                    .font(.system(size: 26))
                    .widgetAccentable()
                VStack(alignment: .leading, spacing: 1) {
                    Text(title).font(.headline).lineLimit(1)
                    Text(detail).font(.caption).lineLimit(1)
                }
                Spacer(minLength: 0)
            }
        }
    }

    private var title: String {
        guard let hold = entry.hold else { return "Ringback" }
        return hold.on ? "Calls on hold" : "Taking calls"
    }

    /// "2 waiting · until 08:15", or "Holds calls at 19:00".
    private var detail: String {
        guard let hold = entry.hold else { return "Open the app once" }
        let time = hold.until?.formatted(date: .omitted, time: .shortened)
        guard hold.on else { return time.map { "Holds calls at \($0)" } ?? "Until you pause" }
        let waiting = entry.waiting == 0 ? "Nothing waiting" : "\(entry.waiting) waiting"
        return waiting + " · " + (time.map { "until \($0)" } ?? "until you take calls")
    }
}
