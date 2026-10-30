import Foundation

let usage = """
    usage: ringback-switchboard serve | ring [--project NAME] [--subject TEXT] [--voice URL] | check | devices

      serve    listen for phone registrations and call events, and every 30 min ask each
               phone to check that it can still reach the switchboard over Nebula
      ring     ring every registered phone, at any time: work hours are for sessions' calls
                 --project NAME  shown as the caller (default: ringback)
                 --subject TEXT  a few words on what the call is about
                 --voice URL     the calling Mac's voice server, e.g. http://10.0.0.1:8788
      check    ask every phone to check its connection now; one that can't reach the
               switchboard shows a warning
      devices  list registered phones, when each last checked in, and their work hours
    """

let home = ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
let configPath = expand("~/.config/ringback/switchboard.toml")
let stateDir = expand("~/.local/state/ringback")

func expand(_ path: String) -> String {
    path.hasPrefix("~/") ? home + path.dropFirst() : path
}

/// Reads switchboard.toml. It only uses `[section]` headers, `key = "value"` lines and `#`
/// comments, so that's all this reads. Keys come back with their section, e.g.
/// "apns.sandbox.key_id".
func loadConfig() -> [String: String] {
    guard let text = try? String(contentsOfFile: configPath, encoding: .utf8) else {
        fail("No config at \(configPath); copy switchboard/switchboard.example.toml there.")
    }
    var config: [String: String] = [:]
    var section = ""
    for raw in text.split(whereSeparator: \.isNewline) {
        let line = raw.trimmingCharacters(in: .whitespaces)
        if line.isEmpty || line.hasPrefix("#") { continue }
        if line.hasPrefix("[") {
            section = line.trimmingCharacters(in: CharacterSet(charactersIn: "[] ")) + "."
            continue
        }
        guard let eq = line.firstIndex(of: "=") else { continue }
        let key = line[..<eq].trimmingCharacters(in: .whitespaces)
        var value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespaces)
        if value.hasPrefix("\""), let end = value.dropFirst().firstIndex(of: "\"") {
            value = String(value[value.index(after: value.startIndex)..<end])
        } else if let hash = value.firstIndex(of: "#") {
            value = value[..<hash].trimmingCharacters(in: .whitespaces)
        }
        config[section + key] = value
    }
    return config
}

/// Reads `--name value` pairs; anything else prints the usage and exits.
func parseOptions(_ arguments: ArraySlice<String>, allowed: Set<String>) -> [String: String] {
    var rest = arguments
    var options: [String: String] = [:]
    while let name = rest.popFirst() {
        guard allowed.contains(name), let value = rest.popFirst() else { fail(usage) }
        options[name] = value
    }
    return options
}

private let clock: DateFormatter = {
    let f = DateFormatter()
    f.dateFormat = "HH:mm:ss"
    return f
}()

func timeOfDay(_ date: Date) -> String { clock.string(from: date) }

func log(_ line: String) {
    print("\(timeOfDay(Date()))  \(line)")
}

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}
