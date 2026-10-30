import SwiftUI

/// The sessions you can call, as the Phone app's Contacts: every session with ringback on, on
/// any of your Macs, by project. Two in the same project are told apart by a second line: what
/// the latest briefing was about, or since when ringback has been on. Tapping one calls it, and
/// its ⓘ opens its details. Fetched from the switchboard each time it shows, and on pull to
/// refresh; a voice server tells the switchboard every 30 s, so the list can be that old.
struct ContactsView: View {
    let state: AppState
    let calls: CallService
    @State private var details: String?

    var body: some View {
        NavigationStack {
            List(state.contacts) { contact in
                HStack(spacing: 8) {
                    Button { calls.callContact(contact) } label: {
                        VStack(alignment: .leading, spacing: 1) {
                            Text(contact.project).font(.body).lineLimit(1)
                            if let second = secondLine(contact) {
                                Text(second).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(RowStyle())
                    .accessibilityHint("Calls \(contact.project)")
                    InfoButton { details = contact.session }
                }
            }
            .listStyle(.plain)
            .overlay {
                if state.contacts.isEmpty {
                    ContentUnavailableView(
                        "No Sessions", systemImage: "person.crop.circle",
                        description: Text("Sessions show here while ringback is on in them. Run /ringback in a session."))
                }
            }
            .navigationTitle("Contacts")
            .refreshable { await state.refreshContacts() }
            .task { await state.refreshContacts() }
            .navigationDestination(item: $details) { session in
                ContactDetailsView(session: session, state: state, calls: calls)
            }
        }
    }

    /// Only where another session has the same project: its latest subject, or since when.
    private func secondLine(_ contact: Contact) -> String? {
        guard state.contacts.contains(where: { $0.project == contact.project && $0.session != contact.session }) else {
            return nil
        }
        if let subject = contact.subject { return subject }
        return contact.since.map { "Ringback on since \($0.recentsLabel)" } ?? "No briefing yet"
    }
}

/// A session's details: its project, a button to call it, what its latest briefing was about
/// and when, since when ringback has been on, and its Mac.
struct ContactDetailsView: View {
    let session: String
    let state: AppState
    let calls: CallService

    var body: some View {
        List {
            if let contact {
                Section {
                    DetailsHeader(name: contact.project, subtitle: nil)
                    ActionButtons {
                        ActionButton(title: "call", systemImage: "phone.fill") { calls.callContact(contact) }
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
            } else {
                Text("This session isn't listening any more.").foregroundStyle(.secondary)
            }
        }
        .listStyle(.insetGrouped)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var contact: Contact? { state.contacts.first { $0.session == session } }
}
