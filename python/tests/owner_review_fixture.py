"""Explicit trusted owner review used by already-consented protocol fixtures."""

def allow_pending(store, owner):
    # These fixtures explicitly act as the owner; production never auto-approves.
    for item in store.inbox(owner)["pending_review"]:
        store.review_preview(item["message_id"], owner)
        store.review_peer(item["message_id"], "approve", owner)
