import subprocess
import time
import requests
import threading
import json
import sys
import os

# Paths
helper_path = os.path.expanduser("~/.agent-comm/bin/agent-comm-helper")
platform_url = "http://8.130.40.38"

def main():
    print("=== Integration Test: Double Daemon P2P Secure Delivery ===")
    
    # 1. Clean and initialize keys directories
    subprocess.run(["rm", "-rf", "/tmp/test_agent1_keys", "/tmp/test_agent2_keys"])
    subprocess.run(["mkdir", "-p", "/tmp/test_agent1_keys", "/tmp/test_agent2_keys"])

    # Init keys to get URNs
    res1 = subprocess.run([helper_path, "init", "/tmp/test_agent1_keys"], capture_output=True, text=True)
    if res1.returncode != 0:
        print(f"Init agent 1 failed: {res1.stderr}")
        sys.exit(1)
    res1_json = json.loads(res1.stdout.strip())
    urn1 = res1_json["urn"]
    peer_id1 = res1_json["peer_id"]
    x25519_pk1 = res1_json["x25519_pubkey"]
    ed25519_pk1 = res1_json["ed25519_pubkey"]
    print(f"Agent 1 URN: {urn1}, PeerID: {peer_id1}")

    res2 = subprocess.run([helper_path, "init", "/tmp/test_agent2_keys"], capture_output=True, text=True)
    if res2.returncode != 0:
        print(f"Init agent 2 failed: {res2.stderr}")
        sys.exit(1)
    res2_json = json.loads(res2.stdout.strip())
    urn2 = res2_json["urn"]
    peer_id2 = res2_json["peer_id"]
    x25519_pk2 = res2_json["x25519_pubkey"]
    ed25519_pk2 = res2_json["ed25519_pubkey"]
    print(f"Agent 2 URN: {urn2}, PeerID: {peer_id2}")

    # 2. Start two daemons
    print("Starting Daemon 1 on port 45042...")
    p1 = subprocess.Popen([helper_path, "daemon", "/tmp/test_agent1_keys", platform_url, "45042"])

    print("Starting Daemon 2 on port 45043...")
    p2 = subprocess.Popen([helper_path, "daemon", "/tmp/test_agent2_keys", platform_url, "45043"])

    # Wait for daemons to boot, join DHT and register URNs
    print("Waiting 15 seconds for daemons to sync and register...")
    time.sleep(15)

    # 2.5 Add mutual contacts via HTTP API
    print("Fetching daemon network addresses...")
    info1 = requests.get("http://localhost:45042/info").json()
    addrs1 = info1.get("addrs", [])
    print(f"Daemon 1 addrs: {addrs1}")

    info2 = requests.get("http://localhost:45043/info").json()
    addrs2 = info2.get("addrs", [])
    print(f"Daemon 2 addrs: {addrs2}")

    print("Adding Agent 2 as contact on Daemon 1...")
    contact_payload1 = {
        "urn": urn2,
        "peer_id": peer_id2,
        "x25519_pk": x25519_pk2,
        "ed25519_pk": ed25519_pk2,
        "display_name": "Agent 2",
        "trusted": True,
        "addrs": addrs2
    }
    requests.post("http://localhost:45042/api/v1/contacts", json=contact_payload1)

    print("Adding Agent 1 as contact on Daemon 2...")
    contact_payload2 = {
        "urn": urn1,
        "peer_id": peer_id1,
        "x25519_pk": x25519_pk1,
        "ed25519_pk": ed25519_pk1,
        "display_name": "Agent 1",
        "trusted": True,
        "addrs": addrs1
    }
    requests.post("http://localhost:45043/api/v1/contacts", json=contact_payload2)

    # 3. Listen on Daemon 2 SSE
    received_messages = []
    sse_ready = threading.Event()
    
    def listen_sse():
        print("Subscribing to Daemon 2 SSE stream...")
        try:
            r = requests.get("http://localhost:45043/api/v1/mq/subscribe", stream=True, timeout=30)
            sse_ready.set()
            for line in r.iter_lines():
                if line:
                    decoded = line.decode('utf-8')
                    if decoded.startswith("data: "):
                        data = json.loads(decoded[6:])
                        print(f"SSE received: {data}")
                        if "sender_urn" in data:
                            received_messages.append(data)
        except Exception as e:
            print(f"SSE listen stopped/error: {e}")

    t = threading.Thread(target=listen_sse, daemon=True)
    t.start()

    # Wait for SSE subscription to connect
    sse_ready.wait(timeout=5)
    time.sleep(2)

    # 4. Send message from Daemon 1 to Agent 2
    print("Sending message from Daemon 1 to Agent 2...")
    payload = {
        "recipient_urn": urn2,
        "text": "Hello Agent 2! This is a direct test message."
    }
    
    try:
        res = requests.post("http://localhost:45042/api/v1/mq/store", json=payload, timeout=20)
        print(f"Send status: {res.status_code}, response: {res.text}")
    except Exception as e:
        print(f"Send POST failed: {e}")

    # Wait for P2P delivery and decryption
    print("Waiting 10 seconds for secure delivery...")
    time.sleep(10)

    # Cleanup
    print("Terminating daemon processes...")
    p1.terminate()
    p2.terminate()
    p1.wait()
    p2.wait()

    # Check received message matches
    print("\n=== Result Verification ===")
    if len(received_messages) > 0:
        expected_senders = {urn1, f"urn:hermes:peer:{peer_id1}"}
        for msg in received_messages:
            sender = msg.get("sender_urn")
            if sender in expected_senders and msg.get("text") == "Hello Agent 2! This is a direct test message.":
                print("🎉 SUCCESS: Message received and content verified matches exactly!")
                sys.exit(0)
        print("❌ FAILURE: Received message did not match expected sender and text.")
        sys.exit(1)
    else:
        print("❌ FAILURE: No messages received in Daemon 2.")
        sys.exit(1)

if __name__ == "__main__":
    main()
