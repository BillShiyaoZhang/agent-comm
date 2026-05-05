import sys
import json
import publish_contact
import register_peer

publish_contact.get_tunnel_url = lambda: "https://mock.trycloudflare.com"
contact = publish_contact.publish_contact("/tmp/test-contact.json")
print("Published contact signature:", contact.get("signature"))
print("Verifying...")
valid = register_peer.verify_contact(contact)
print("Is valid?", valid)
