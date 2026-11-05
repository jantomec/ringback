import SwiftUI

/// What sessions showed you, in Recents ("Showing you things" in docs/design.md): a shelf of
/// small pictures between the waiting calls and the history, newest first, with a dot on each
/// you haven't opened, and See All for all of them, by day. Not a conversation: a picture opens
/// what it shows, and nothing is answered.
struct ShownShelf: View {
    let state: AppState
    let seeAll: () -> Void

    /// How many the shelf shows; See All has the rest.
    static let count = 12

    var body: some View {
        Section {
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: 12) {
                    ForEach(state.shown.prefix(Self.count)) { item in
                        ShownTile(item: item, state: state, size: 92)
                    }
                }
                .padding(.horizontal, 16)
            }
            .listRowInsets(EdgeInsets(top: 4, leading: 0, bottom: 10, trailing: 0))
            .listRowSeparator(.hidden)
        } header: {
            HStack {
                Text("Shown")
                Spacer()
                Button("See All", action: seeAll).font(.subheadline).textCase(nil)
            }
        }
    }
}

/// All that sessions showed you, kept for a week: by day, newest first, and by project if you
/// pick one. Or what one session showed you, from its card in Sessions, with a button to call
/// it while it listens.
struct ShownGallery: View {
    let state: AppState
    var session: String?
    var call: (() -> Void)?
    @State private var project: String?

    private var items: [ShownItem] {
        state.shown.filter { (session == nil || $0.session == session) && (project == nil || $0.project == project) }
    }

    /// The session's project, or "Shown" for everything.
    private var title: String {
        guard let session else { return String(localized: "Shown") }
        return state.contacts.first { $0.session == session }?.project ?? state.shown.first { $0.session == session }?.project
            ?? String(localized: "Shown")
    }

    var body: some View {
        ScrollView {
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 104), spacing: 12, alignment: .top)], spacing: 16) {
                ForEach(days, id: \.day) { day in
                    Section {
                        ForEach(day.items) { item in ShownTile(item: item, state: state, size: 104) }
                    } header: {
                        Text(day.day.dayLabel).font(.headline).frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                    }
                }
            }
            .padding(.horizontal, 16)
            if !items.isEmpty {
                Text("Your hub keeps what sessions show you for a week. To keep one, share it.")
                    .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center).padding()
            }
        }
        .overlay {
            if items.isEmpty {
                ContentUnavailableView(
                    "Nothing Shown", systemImage: "photo.on.rectangle",
                    description: Text(session == nil
                        ? "Images, PDFs, clips and models your sessions show you are here for a week."
                        : "Images, PDFs, clips and models this session shows you are here for a week."))
            }
        }
        .navigationTitle(title)
        .toolbar {
            if let call {
                Button("Call", systemImage: "phone.fill", action: call)
            }
            let projects = Array(Set(state.shown.map(\.project))).sorted()
            if session == nil, projects.count > 1 {
                Menu {
                    Picker("Project", selection: $project) {
                        Text("All Projects").tag(String?.none)
                        ForEach(projects, id: \.self) { Text($0).tag(String?.some($0)) }
                    }
                } label: {
                    Label("Project", systemImage: project == nil ? "line.3.horizontal.decrease.circle" : "line.3.horizontal.decrease.circle.fill")
                }
            }
        }
        .refreshable { await state.refreshShown() }
    }

    /// The items by the day they were shown, newest first.
    private var days: [(day: Date, items: [ShownItem])] {
        let calendar = Calendar.current
        let grouped = Dictionary(grouping: items) { calendar.startOfDay(for: $0.at) }
        return grouped.keys.sorted(by: >).map { ($0, grouped[$0] ?? []) }
    }
}

/// One shown item as a small picture: the project and when under it, a dot until you open it,
/// and a mark for a PDF, a clip or a model. Tapping it opens it.
struct ShownTile: View {
    let item: ShownItem
    let state: AppState
    let size: CGFloat

    var body: some View {
        Button {
            state.open(item)
        } label: {
            VStack(alignment: .leading, spacing: 3) {
                ShownThumbnail(item: item)
                    .frame(width: size, height: size)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
                    .overlay(alignment: .bottomTrailing) {
                        if let mark = Self.mark(item.kind) {
                            Image(systemName: mark).font(.caption.weight(.semibold)).foregroundStyle(.white)
                                .padding(5).background(.black.opacity(0.45), in: Circle()).padding(5)
                        }
                    }
                HStack(spacing: 4) {
                    if !state.hasSeen(item) {
                        Circle().fill(.tint).frame(width: 7, height: 7)
                    }
                    Text(item.project).font(.caption).lineLimit(1)
                }
                Text(item.at.formatted(date: .omitted, time: .shortened)).font(.caption2).foregroundStyle(.secondary)
            }
            .frame(width: size, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel("\(item.title), from \(item.project), \(item.at.formatted(date: .omitted, time: .shortened))")
        .accessibilityValue(state.hasSeen(item) ? "" : String(localized: "New"))
    }

    /// The mark on a picture that isn't an image itself.
    static func mark(_ kind: ShownItem.Kind) -> String? {
        switch kind {
        case .image: nil
        case .pdf: "doc.fill"
        case .video: "play.fill"
        case .model: "cube.fill"
        }
    }
}

/// An item's thumbnail, from the hub or kept on the phone; its kind's symbol until then, or if
/// it has none.
struct ShownThumbnail: View {
    let item: ShownItem
    @State private var image: UIImage?

    var body: some View {
        ZStack {
            Rectangle().fill(Color(.secondarySystemFill))
            if let image {
                Image(uiImage: image).resizable().scaledToFill()
            } else {
                Image(systemName: ShownSection.symbol(item.kind)).font(.title2).foregroundStyle(.secondary)
            }
        }
        .task(id: item.id) {
            if let file = try? await ShownFiles.fetch(item.id, part: "thumb", into: ShownView.folder, timeout: 20) {
                image = UIImage(contentsOfFile: file.path)
            }
        }
    }
}
