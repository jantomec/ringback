"""ringback-switchboard serve | ring [--project NAME] | devices"""

import argparse
import uuid

from . import devices, load_config


def main() -> None:
    p = argparse.ArgumentParser(prog="ringback-switchboard")
    sub = p.add_subparsers(dest="command", required=True)
    sub.add_parser("serve", help="listen for phone registrations and call events")
    ring = sub.add_parser("ring", help="ring every registered phone")
    ring.add_argument("--project", default="ringback", help="name shown as the caller")
    sub.add_parser("devices", help="list registered phones")
    args = p.parse_args()

    if args.command == "devices":
        for token, d in devices.load().items():
            print(f"…{token[-8:]}  {d['device']}  {d['environment']}  registered {d['registered']}")
        return

    config = load_config()
    if args.command == "serve":
        from .server import serve

        serve(config["listen"])
    elif args.command == "ring":
        from .apns import APNs

        known = devices.load()
        if not known:
            raise SystemExit("No phone registered yet: open the app and enter the switchboard address.")
        clients = {}
        for token, d in known.items():
            env = d["environment"]
            if env not in clients:
                if env not in config["apns"]:
                    print(f"skipping {d['device']}: no [apns.{env}] key in the config")
                    continue
                c = config["apns"]
                clients[env] = APNs(c["team_id"], c[env]["key_id"], c[env]["key_file"], c["bundle_id"])
            call_id = str(uuid.uuid4())
            status, reason = clients[env].push(token, env, {"call_id": call_id, "project": args.project})
            print(f"call {call_id[:8]} → {d['device']}: APNs {status} {reason}".rstrip())
            if reason in ("BadDeviceToken", "Unregistered"):
                devices.forget(token)
                print("  token no longer valid; forgotten — reopen the app to register again")


if __name__ == "__main__":
    main()
