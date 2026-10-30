// The ringback switchboard: knows the phone, places calls through APNs, hears what happened.
import Foundation

setlinebuf(stdout)  // one log line at a time, also when launchd writes it to a file

let arguments = CommandLine.arguments.dropFirst()
switch arguments.first {
case "serve":
    guard let listen = loadConfig()["listen"] else { fail("No listen address in \(configPath).") }
    serve(listen: listen)
case "ring":
    let options = parseOptions(arguments.dropFirst(), allowed: ["--project", "--subject", "--voice"])
    if Devices.load().isEmpty { fail("No phone registered yet: open the app and enter the switchboard address.") }
    let lines = await ring(project: options["--project"] ?? "ringback", subject: options["--subject"], voice: options["--voice"])
    lines.forEach { print($0) }
case "check":
    let lines = await askPhonesToCheck(config: loadConfig())
    if lines.isEmpty { fail("No phone has given a notification token yet: open the app and allow notifications.") }
    lines.forEach { print($0) }
case "devices":
    for (token, d) in Devices.load().sorted(by: { $0.key < $1.key }) {
        let checks = d["notification_token"] == nil ? "" : d["checked_in"].map { "  checked in \($0)" } ?? "  never checked in"
        let hours = describe(d).map { "  \($0)" } ?? ""
        print("…\(token.suffix(8))  \(d["device"] ?? "")  \(d["environment"] ?? "")  registered \(d["registered"] ?? "")\(checks)\(hours)")
    }
case "-h", "--help":
    print(usage)
default:
    fail(usage)
}
