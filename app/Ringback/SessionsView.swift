import SwiftUI

/// The sessions you can call: every session with ringback on, on any of your Macs, by
/// project. There are a handful, not hundreds as in the Phone app's Contacts, so each is a
/// card: what its latest briefing was about and when, whether a call of its waits, and how
/// many of the things it showed you you haven't opened. Tapping a card opens what it showed
/// you; its green phone calls it; holding it offers its details. Fetched from the hub every
/// 3 s while it shows, and on pull to refresh; an agent tells the hub within a
/// second of a session starting or stopping to listen, and every 30 s.
struct SessionsView: View {
    let state: AppState
    let calls: CallService
    @Environment(\.scenePhase) private var scenePhase
    @State private var opened: Opened?

    /// What a card opens: what its session showed you, or its details.
    enum Opened: Hashable {
        case shown(String)
        case details(String)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(spacing: 12) {
                    ForEach(state.contacts) { contact in
                        SessionCard(
                            contact: contact, waiting: state.waiting.first { $0.session == contact.session },
                            unseen: state.shown.filter { $0.session == contact.session && !state.hasSeen($0) }.count,
                            open: { opened = .shown(contact.session) }, call: { calls.callContact(contact) },
                            canCall: !calls.onCall
                        )
                        .contentShape(.contextMenuPreview, .rect(cornerRadius: SessionCard.corner))
                        .contextMenu {
                            Button("Call", systemImage: "phone.fill") { calls.callContact(contact) }
                                .disabled(calls.onCall)
                            Button("Details", systemImage: "info.circle") { opened = .details(contact.session) }
                        }
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
            }
            .background(Color(.systemGroupedBackground))
            .overlay {
                if state.contacts.isEmpty {
                    ContentUnavailableView(
                        "No Sessions", systemImage: "terminal",
                        description: Text("Sessions show here while ringback is on in them. Run /ringback in a session."))
                }
            }
            .navigationTitle("Sessions")
            .toolbar { ToolbarItem(placement: .topBarTrailing) { SettingsButton(state: state) } }
            .refreshable { await state.refreshContacts() }
            // While it shows and the app is in front: the task ends when either stops.
            .task(id: scenePhase) {
                guard scenePhase == .active else { return }
                while !Task.isCancelled {
                    await state.refreshContacts()
                    await state.refreshWaiting()
                    try? await Task.sleep(for: .seconds(3))
                }
            }
            .navigationDestination(item: $opened) { opened in
                switch opened {
                case .shown(let session):
                    ShownGallery(state: state, session: session, call: calls.onCall ? nil : state.contacts.first { $0.session == session }.map { contact in
                        { calls.callContact(contact) }
                    })
                case .details(let session):
                    SessionDetailsView(session: session, state: state, calls: calls)
                }
            }
        }
    }
}

/// A session as a card: its project's monogram, with how many things it showed you you
/// haven't opened, its project, what its latest briefing was about, when, and a call of its
/// that waits. Tapping it opens what it showed you; its green phone calls it.
private struct SessionCard: View {
    static let corner: CGFloat = 22
    let contact: Contact
    let waiting: WaitingCall?
    let unseen: Int
    let open: () -> Void
    let call: () -> Void
    /// Not while you're on a call.
    var canCall = true

    var body: some View {
        HStack(spacing: 14) {
            Button(action: open) { summary }
                .buttonStyle(CardStyle())
                .accessibilityIdentifier("Session")
                .accessibilityHint("Opens what \(contact.project) showed you")
            Button(action: call) {
                Image(systemName: "phone.fill")
                    .font(.title3)
                    .foregroundStyle(.white)
                    .frame(width: 46, height: 46)
                    .background(.green.gradient, in: .circle)
            }
            .disabled(!canCall)
            .opacity(canCall ? 1 : 0.4)
            .buttonStyle(CardStyle())
            .accessibilityLabel("Call \(contact.project)")
        }
        .foregroundStyle(.primary)
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground), in: .rect(cornerRadius: Self.corner))
    }

    private var summary: some View {
        HStack(spacing: 14) {
            Monogram(name: contact.project)
                .overlay(alignment: .topTrailing) {
                    if unseen > 0 {
                        Text(unseen, format: .number)
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.white)
                            .padding(.horizontal, 5)
                            .frame(minWidth: 20, minHeight: 20)
                            .background(.red, in: .capsule)
                            .offset(x: 4, y: -4)
                    }
                }
            VStack(alignment: .leading, spacing: 4) {
                Text(contact.project).font(.title3.weight(.semibold)).lineLimit(1)
                Text(contact.subject ?? "No briefing yet")
                    .font(.subheadline)
                    .foregroundStyle(contact.subject == nil ? .tertiary : .secondary)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 12) { when; waitingLabel }
                    VStack(alignment: .leading, spacing: 2) { when; waitingLabel }
                }
                .font(.footnote)
                .padding(.top, 2)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityValue(unseen > 0 ? "\(unseen) new" : "")
    }

    private var when: some View {
        Group {
            if let at = contact.briefedAt {
                Label(Calendar.current.isDateInToday(at) ? "Briefed at \(Self.time(at))" : "Briefed \(Self.time(at))",
                      systemImage: "text.bubble")
            } else if let since = contact.since {
                Label("Ringback on since \(Self.time(since))", systemImage: "power")
            }
        }
        .foregroundStyle(.secondary)
        .lineLimit(1)
    }

    @ViewBuilder private var waitingLabel: some View {
        if let waiting {
            Label(waiting.question ? "Question waiting" : "Call waiting", systemImage: "clock.fill")
                .foregroundStyle(.orange)
                .fontWeight(.medium)
                .lineLimit(1)
        }
    }

    /// "14:32" today, otherwise with the day: "Thu 14:32".
    private static func time(_ date: Date) -> String {
        if Calendar.current.isDateInToday(date) { return date.formatted(date: .omitted, time: .shortened) }
        return date.formatted(.dateTime.weekday(.abbreviated).hour().minute())
    }
}

/// A project's initials on a circle in its own colour, as the Phone app shows a contact
/// without a photo: "ringback-trial" is RT. The colour comes from the name, so it stays.
private struct Monogram: View {
    let name: String
    private static let colors: [Color] = [.blue, .indigo, .purple, .pink, .red, .orange, .green, .mint, .teal, .cyan, .brown]

    var body: some View {
        Text(initials)
            .font(.title3.weight(.semibold))
            .foregroundStyle(.white)
            .frame(width: 52, height: 52)
            .background(color.gradient, in: .circle)
            .accessibilityHidden(true)
    }

    private var initials: String {
        let words = name.split { !$0.isLetter && !$0.isNumber }
        return String(words.prefix(2).compactMap(\.first)).uppercased()
    }

    /// FNV-1a of the name: the same colour on every launch, unlike `hashValue`.
    private var color: Color {
        let hash = name.utf8.reduce(UInt32(2_166_136_261)) { ($0 ^ UInt32($1)) &* 16_777_619 }
        return Self.colors[Int(hash % UInt32(Self.colors.count))]
    }
}

/// A card you tap: it shrinks a little while pressed.
private struct CardStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed ? 0.97 : 1)
            .opacity(configuration.isPressed ? 0.85 : 1)
            .animation(.spring(duration: 0.2), value: configuration.isPressed)
    }
}

/// A session's details: its project, a button to call it, what its latest briefing was about
/// and when, since when ringback has been on, and its Mac.
struct SessionDetailsView: View {
    let session: String
    let state: AppState
    let calls: CallService

    var body: some View {
        List {
            if let contact {
                Section {
                    DetailsHeader(name: contact.project, subtitle: nil)
                    ActionButtons {
                        ActionButton(title: "call", systemImage: "phone.fill", enabled: !calls.onCall) { calls.callContact(contact) }
                    }
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                Section("Latest Briefing") {
                    if let subject = contact.subject {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(subject)
                            if let at = contact.briefedAt {
                                Text("\(at.dayLabel), \(at.formatted(date: .omitted, time: .shortened))")
                                    .font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                    } else {
                        Text("None yet").foregroundStyle(.secondary)
                    }
                }
                Section {
                    if let since = contact.since {
                        LabeledContent("Ringback On Since", value: "\(since.dayLabel), \(since.formatted(date: .omitted, time: .shortened))")
                    }
                    LabeledContent("Mac", value: contact.voice.host() ?? contact.voice.absoluteString)
                    LabeledContent("Session", value: String(contact.session.prefix(8)))
                }
                ShownSection(items: state.shown.filter { $0.session == contact.session }, state: state)
            } else {
                Text("This session isn't listening any more.").foregroundStyle(.secondary)
                ShownSection(items: state.shown.filter { $0.session == session }, state: state)
            }
        }
        .listStyle(.insetGrouped)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var contact: Contact? { state.contacts.first { $0.session == session } }
}
