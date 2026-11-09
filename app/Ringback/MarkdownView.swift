import SwiftUI

/// Text a session sent you, as Markdown ("Showing you things" in docs/design.md), drawn block by
/// block from Foundation's parser: headings, paragraphs, lists, quotes, code with a Copy
/// button, rules and tables. Only web and mail links open (in Safari, or the app that claims
/// them); any other, such as a file's path, shows as code. Code and tables scroll sideways
/// rather than wrap, so a command stays on one line.
struct MarkdownView: View {
    let source: String

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 12) {
                ForEach(MarkdownBlock.parse(source)) { block in
                    MarkdownBlockView(block: block)
                }
            }
            .padding()
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// One block of Markdown, with its inline text as SwiftUI draws it (bold, italic, code, links).
struct MarkdownBlock: Identifiable {
    enum Kind {
        case heading(Int)
        case paragraph
        /// A list item's first paragraph has its marker; a later one in the same item, none.
        case item(marker: String, depth: Int)
        case code(language: String?)
        case rule
        case table(rows: [[AttributedString]])
    }

    let id: Int
    let kind: Kind
    let text: AttributedString
    /// In a quote, drawn with a bar beside it.
    let quoted: Bool

    /// The blocks of source; one paragraph of it as it is if it isn't Markdown.
    static func parse(_ source: String) -> [MarkdownBlock] {
        let options = AttributedString.MarkdownParsingOptions(
            allowsExtendedAttributes: false, interpretedSyntax: .full, failurePolicy: .returnPartiallyParsedIfPossible)
        guard let parsed = try? AttributedString(markdown: source, options: options) else {
            return [MarkdownBlock(id: 0, kind: .paragraph, text: AttributedString(source), quoted: false)]
        }
        let text = webLinksOnly(parsed)
        var blocks: [MarkdownBlock] = []
        var tables: [Int: (index: Int, rows: [Int: [Int: AttributedString]])] = [:]
        var lastItem: Int?
        for (intent, range) in text.runs[\.presentationIntent] {
            let components = intent?.components ?? []
            let quoted = components.contains { if case .blockQuote = $0.kind { true } else { false } }
            let slice = AttributedString(text[range])
            // A table: each cell goes in its row, by its column.
            if let table = components.first(where: { if case .table = $0.kind { true } else { false } }) {
                var row = 0, column = 0
                for component in components {
                    switch component.kind {
                    case .tableCell(let index): column = index
                    case .tableRow(let index): row = index + 1
                    case .tableHeaderRow: row = 0
                    default: break
                    }
                }
                if tables[table.identity] == nil {
                    tables[table.identity] = (blocks.count, [:])
                    blocks.append(MarkdownBlock(id: blocks.count, kind: .table(rows: []), text: "", quoted: quoted))
                }
                tables[table.identity]?.rows[row, default: [:]][column, default: ""] += slice
                continue
            }
            guard let inner = components.first else {
                blocks.append(MarkdownBlock(id: blocks.count, kind: .paragraph, text: slice, quoted: quoted))
                continue
            }
            let kind: Kind
            switch inner.kind {
            case .header(let level): kind = .heading(level)
            case .codeBlock(let language):
                kind = .code(language: language.flatMap { $0.isEmpty ? nil : $0 })
            case .thematicBreak: kind = .rule
            default:
                if let item = components.first(where: { if case .listItem = $0.kind { true } else { false } }),
                    case .listItem(let ordinal) = item.kind
                {
                    let lists = components.filter {
                        switch $0.kind {
                        case .orderedList, .unorderedList: true
                        default: false
                        }
                    }
                    let ordered = lists.first.map { if case .orderedList = $0.kind { true } else { false } } ?? false
                    let marker = lastItem == item.identity ? "" : ordered ? "\(ordinal)." : "•"
                    lastItem = item.identity
                    kind = .item(marker: marker, depth: max(lists.count, 1))
                } else {
                    kind = .paragraph
                }
            }
            var body = slice
            if case .code = kind, body.characters.last == "\n" {
                body.characters.removeLast()
            }
            blocks.append(MarkdownBlock(id: blocks.count, kind: kind, text: body, quoted: quoted))
        }
        for (_, table) in tables {
            let rows = table.rows.keys.sorted().map { row in
                let cells = table.rows[row] ?? [:]
                return cells.keys.sorted().map { cells[$0] ?? "" }
            }
            let old = blocks[table.index]
            blocks[table.index] = MarkdownBlock(id: old.id, kind: .table(rows: rows), text: "", quoted: old.quoted)
        }
        return blocks
    }

    /// The text with only web and mail links left: any other, as a file's path, becomes code.
    private static func webLinksOnly(_ text: AttributedString) -> AttributedString {
        var text = text
        for (link, range) in text.runs[\.link] {
            guard let link, !["http", "https", "mailto"].contains(link.scheme?.lowercased() ?? "") else { continue }
            text[range].link = nil
            text[range].inlinePresentationIntent = .code
        }
        return text
    }
}

/// A block as it's drawn: its text selectable, so it can be copied.
struct MarkdownBlockView: View {
    let block: MarkdownBlock

    var body: some View {
        content
            .textSelection(.enabled)
            .padding(.leading, block.quoted ? 12 : 0)
            .overlay(alignment: .leading) {
                if block.quoted {
                    RoundedRectangle(cornerRadius: 1.5).fill(.tertiary).frame(width: 3)
                }
            }
            .foregroundStyle(block.quoted ? .secondary : .primary)
    }

    @ViewBuilder
    private var content: some View {
        switch block.kind {
        case .heading(let level):
            Text(block.text).font(level == 1 ? .title2.bold() : level == 2 ? .title3.bold() : .headline)
                .accessibilityAddTraits(.isHeader)
        case .paragraph:
            Text(block.text)
        case .item(let marker, let depth):
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(marker).monospacedDigit().frame(minWidth: 16, alignment: .trailing)
                Text(block.text)
            }
            .padding(.leading, CGFloat(depth - 1) * 18)
        case .code(let language):
            CodeBlock(code: String(block.text.characters), language: language)
        case .rule:
            Divider()
        case .table(let rows):
            ScrollView(.horizontal) {
                Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 6) {
                    ForEach(Array(rows.enumerated()), id: \.offset) { index, cells in
                        GridRow {
                            ForEach(Array(cells.enumerated()), id: \.offset) { _, cell in
                                Text(cell).font(index == 0 ? .subheadline.bold() : .subheadline)
                            }
                        }
                        if index == 0 { Divider() }
                    }
                }
            }
        }
    }
}

/// A block of code: its language and a Copy button above it, the code in a monospaced font,
/// scrolling sideways.
struct CodeBlock: View {
    let code: String
    let language: String?
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(language ?? "").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button {
                    UIPasteboard.general.string = code
                    copied = true
                    Task {
                        try? await Task.sleep(for: .seconds(1.5))
                        copied = false
                    }
                } label: {
                    Label(copied ? "Copied" : "Copy", systemImage: copied ? "checkmark" : "doc.on.doc").font(.caption)
                }
                .sensoryFeedback(.success, trigger: copied) { _, now in now }
            }
            ScrollView(.horizontal) {
                Text(code).font(.callout.monospaced()).fixedSize()
            }
        }
        .padding(10)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 8))
    }
}

/// Text that's only a link: where it goes, all of it, with Open Link and Copy Link.
struct LinkCard: View {
    let url: URL
    let title: String
    @Environment(\.openURL) private var openURL

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 6) {
                Label(url.host() ?? url.absoluteString, systemImage: "link").font(.headline)
                if !title.isEmpty, title != url.absoluteString {
                    Text(title)
                }
                Text(url.absoluteString).font(.footnote).foregroundStyle(.secondary).textSelection(.enabled)
            }
            .padding()
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12))
            Button {
                openURL(url)
            } label: {
                Text("Open Link").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent).controlSize(.large)
            Button("Copy Link", systemImage: "doc.on.doc") { UIPasteboard.general.url = url }
                .buttonStyle(.bordered)
        }
        .padding()
        .frame(maxHeight: .infinity, alignment: .top)
    }
}
