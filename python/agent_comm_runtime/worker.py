"""Finite deterministic meeting worker: task permission plus native policy approval.

No HostSession is fabricated. TaskRunContext is an internal capability limited
to one persisted policy revision and run; it has no confirmation/model port.
"""
from dataclasses import dataclass
import json

from .policy import validate_action


@dataclass(frozen=True)
class TaskRunContext:
    principal_id: str
    task_id: str
    task_revision: int
    policy_revision: int
    run_id: str

    @property
    def owner_session(self):
        return self.principal_id + "|worker:" + self.run_id


class WorkerMixin:
    def _worker_policy_current(self, record):
        from .store import instant
        task = self._get("task", record["task_id"])
        return bool(task and self._belongs(task, record["owner_session"])
                    and self._live(task) and task["revision"] == record["task_revision"]
                    and instant(record["policy"]["expires_at"]) > self.clock())

    def _worker_view(self, task):
        record = self._get("worker_policy", task["task_id"])
        if not record:
            return {"status": "disabled"}
        status = record["status"]
        if status in {"active", "pending", "paused"} and not self._worker_policy_current(record):
            status = "expired"
        elif status == "active" and (record["runs_used"] >= record["policy"]["max_runs"] or record["sends_used"] >= record["policy"]["max_sends"]):
            status = "exhausted"
        return {**{k: record[k] for k in ("policy", "policy_revision", "runs_used", "sends_used", "waiting_reason", "last_run_at")}, "status": status}

    def prepare_worker_policy(self, task_id, policy, owner_session):
        from .store import canonical, digest, instant
        self._owner(owner_session)
        keys = {"collaboration_id", "allow_propose", "allow_accept", "proposal", "max_runs", "max_sends", "interval_seconds", "expires_at"}
        if not isinstance(policy, dict) or set(policy) != keys:
            raise ValueError("Worker policy requires an exact collaboration, actions, proposal, run/send limits, interval and expiry")
        from .collaboration_v2 import identifier
        identifier(policy["collaboration_id"])
        if any(type(policy[k]) is not bool for k in ("allow_propose", "allow_accept")) or not (policy["allow_propose"] or policy["allow_accept"]):
            raise ValueError("Explicitly choose at least one meeting capability")
        for key, lower, upper in (("max_runs", 1, 100), ("max_sends", 1, 32), ("interval_seconds", 15, 3600)):
            if type(policy[key]) is not int or not lower <= policy[key] <= upper:
                raise ValueError(f"{key} must be an integer from {lower} to {upper}")
        with self._transaction():
            task = self._task(task_id, owner_session)
            if not self._live(task) or not self.clock() < instant(policy["expires_at"]) <= instant(task["scope"]["expires_at"]):
                raise ValueError("Worker requires a live task and may not outlive its mandate")
            c = self._v2_collaboration(policy["collaboration_id"], owner_session)
            if c["task_id"] != task_id or not c.get("joined") or c.get("agreement"):
                raise ValueError("Worker requires this task's joined, unfinished collaboration")
            for flag, capability in (("allow_propose", "propose_meeting"), ("allow_accept", "accept_meeting")):
                if policy[flag] and capability not in task["scope"]["capabilities"]:
                    raise ValueError("Worker cannot add a capability absent from the mandate")
            if policy["allow_propose"]:
                if c["initiator_urn"] != self.local_urn:
                    raise ValueError("Only the collaboration initiator may automatically propose")
                action = validate_action({"capability": "propose_meeting", "recipient_ids": [c["peer_id"]], "payload": policy["proposal"]})
                if self._evaluate_current(task, action)["decision"] != "allow":
                    raise ValueError("The fixed worker proposal must already fit the task mandate")
                if action["payload"]["version"] != 1:
                    raise ValueError("The finite worker only publishes the first proposal")
                policy = {**policy, "proposal": action["payload"]}
            elif policy["proposal"] is not None:
                raise ValueError("A proposal is accepted only when allow_propose is enabled")
            existing = self._get("worker_policy", task_id)
            if existing and existing["policy"] == policy and existing["status"] == "pending":
                approval = self._get("approval", existing["approval_id"])
                return {"decision": "ask", **self._public_approval(approval)}
            revision = (existing or {}).get("policy_revision", 0) + 1
            record = {"task_id": task_id, "owner_session": owner_session, "owner_id": self._principal(owner_session),
                      "task_revision": task["revision"], "policy_revision": revision, "policy": json.loads(canonical(policy)),
                      "status": "pending", "runs_used": 0, "sends_used": 0, "last_run_at": None, "waiting_reason": None}
            self._put("worker_policy", task_id, record)
            question = ("是否允许这项任务的有限后台程序自动推进线上会议方案？\n"
                        + "原任务范围：\n" + canonical(task["scope"]) + "\n后台策略：\n" + canonical(policy)
                        + "\nallow_propose 只允许上述固定方案；allow_accept 允许接受符合原任务范围的任一当前方案。"
                        "运行次数与发送次数均有限；发送预算也计入既有许可的固定协议回执。"
                        "不自动加入新协作、不任意发消息、不读取私人记忆、不调用模型或外部工具、不创建日历。"
                        "例外或发送结果不确定即暂停并留下待办；暂停后重新启用或改变策略均需要新的原生确认。")
            request = self._approval("worker_policy", task_id, owner_session,
                {"policy_revision": revision, "task_revision": task["revision"], "policy_hash": digest(policy)},
                question, min(instant(policy["expires_at"]), self.clock() + 900))
            record["approval_id"] = request["approval_id"]
            self._put("worker_policy", task_id, record)
            return {"decision": "ask", **request}

    def _worker_approval_current(self, approval):
        from .store import digest
        record = self._get("worker_policy", approval["subject_id"])
        return bool(record and record["status"] == "pending" and self._worker_policy_current(record)
                    and record["policy_revision"] == approval["payload"]["policy_revision"]
                    and record["task_revision"] == approval["payload"]["task_revision"]
                    and digest(record["policy"]) == approval["payload"]["policy_hash"])

    def _worker_apply_approval(self, approval):
        record = self._get("worker_policy", approval["subject_id"])
        record["status"] = "active"
        self._put("worker_policy", record["task_id"], record)

    def pause_worker(self, task_id, owner_session, *, revoke=False):
        self._owner(owner_session)
        with self._transaction():
            task = self._task(task_id, owner_session)
            record = self._get("worker_policy", task_id)
            if record:
                record.update(status="revoked" if revoke else "paused", waiting_reason="owner_revoked" if revoke else "owner_paused")
                self._put("worker_policy", task_id, record)
                self._worker_attention(record)
            return {"task_id": task_id, "worker": self._worker_view(task)}

    def _worker_attention(self, record):
        reason = record.get("waiting_reason")
        needs_owner = reason in {"exception_requires_owner", "send_uncertain", "budget_exhausted", "state_changed", "worker_error"}
        if not needs_owner and not any(i.get("source_kind") == "worker_policy" and i.get("source_id") == record["task_id"] for i in self._all("attention")):
            return
        state = "open" if needs_owner else "resolved"
        if record["status"] == "revoked":
            state = "superseded"
        elif not self._worker_policy_current(record):
            state = "expired"
        self._attention_put(record["owner_id"], "worker_policy", record["task_id"], kind="needs_recovery",
                            subject_id=record["task_id"], task_id=record["task_id"],
                            title="有限后台协作需要你处理", summary="请查看任务的当前状态、运行预算与待决定问题。",
                            state=state, source_revision=str(record["policy_revision"]) + ":" + str(reason))

    def _check_worker_context(self, context):
        if not isinstance(context, TaskRunContext):
            raise ValueError("A bounded worker run context is required")
        record = self._get("worker_policy", context.task_id)
        run = self._get("worker_run", context.run_id)
        if (not record or record["owner_id"] != context.principal_id or record["status"] != "active"
                or record["policy_revision"] != context.policy_revision or record["task_revision"] != context.task_revision
                or not self._worker_policy_current(record) or not run or run["status"] != "running"
                or run["policy_revision"] != context.policy_revision or run["task_id"] != context.task_id):
            raise ValueError("Worker policy was paused, revoked, expired or replaced")
        return record, run

    def _reserve_worker_send(self, context, operation_id):
        record, run = self._check_worker_context(context)
        if run.get("operation_id") not in (None, operation_id):
            raise ValueError("One worker run can send at most one operation")
        if run.get("operation_id") == operation_id:
            return
        if record["sends_used"] >= record["policy"]["max_sends"]:
            raise ValueError("Worker send budget exhausted")
        record["sends_used"] += 1
        run["operation_id"] = operation_id
        self._put("worker_policy", context.task_id, record)
        self._put("worker_run", context.run_id, run)


def run_worker_tick(store, principal_id, transport, *, max_tasks=10):
    """Called by a trusted host timer. No inbound/model parameter chooses the principal."""
    from .collaboration_v2 import MAINTENANCE, digest
    if not isinstance(principal_id, str) or not principal_id or "|" in principal_id:
        raise ValueError("A trusted profile principal is required")
    if type(max_tasks) is not int or not 1 <= max_tasks <= 10:
        raise ValueError("Worker tick handles at most ten tasks")
    with store._transaction():
        policies = sorted((r for r in store._all("worker_policy") if r["owner_id"] == principal_id and r["status"] == "active"
                           and store._worker_policy_current(r)
                           and (r["last_run_at"] is None or r["last_run_at"] + r["policy"]["interval_seconds"] <= store.clock())),
                          key=lambda r: r["last_run_at"] or 0)[:max_tasks]
    results = []
    for saved in policies:
        context = None
        try:
            with store._transaction():
                policy = store._get("worker_policy", saved["task_id"])
                if not policy or policy["status"] != "active" or not store._worker_policy_current(policy):
                    continue
                if policy["last_run_at"] is not None and policy["last_run_at"] + policy["policy"]["interval_seconds"] > store.clock():
                    continue
                active_runs = [r for r in store._all("worker_run") if r["task_id"] == policy["task_id"]
                               and r["policy_revision"] == policy["policy_revision"] and r["status"] == "running"]
                if active_runs and any(r["started_at"] + 60 > store.clock() for r in active_runs):
                    continue
                if active_runs:
                    # Crash recovery is inspection, never an automatic replay.
                    policy.update(status="paused", waiting_reason="send_uncertain")
                    store._put("worker_policy", policy["task_id"], policy)
                    store._worker_attention(policy)
                    continue
                if policy["runs_used"] >= policy["policy"]["max_runs"] or policy["sends_used"] >= policy["policy"]["max_sends"]:
                    policy.update(status="paused", waiting_reason="budget_exhausted")
                    store._put("worker_policy", policy["task_id"], policy)
                    store._worker_attention(policy)
                    continue
                policy["runs_used"] += 1
                policy["last_run_at"] = store.clock()
                run_id = "worker-" + digest([principal_id, policy["task_id"], policy["policy_revision"], policy["runs_used"]])[:40]
                context = TaskRunContext(principal_id, policy["task_id"], policy["task_revision"], policy["policy_revision"], run_id)
                store._put("worker_run", run_id, {"run_id": run_id, "task_id": policy["task_id"], "policy_revision": policy["policy_revision"], "status": "running", "started_at": store.clock()})
                store._put("worker_policy", policy["task_id"], policy)
                c = store._v2_collaboration(policy["policy"]["collaboration_id"], context.owner_session)
                operations = [o for o in store._all("v2_operation") if o["collaboration_id"] == c["collaboration_id"]]
                uncertain = any(o["status"] == "sending" for o in operations)
                maintenance = next((o for o in operations if o["maintenance"] and o["status"] == "ready"), None)
                kind, payload, operation_id = None, None, None
                if uncertain:
                    reason = "send_uncertain"
                elif maintenance:
                    operation_id, reason = maintenance["operation_id"], None
                elif c.get("phase") == "closed":
                    reason = "collaboration_complete"
                elif not c.get("joined") or c.get("agreement") or c.get("cancel_request") or c.get("withdraw_pending") or c.get("waiting_reason"):
                    reason = "waiting_for_peer"
                elif not c.get("terms") and policy["policy"]["allow_propose"]:
                    kind, payload, reason = "proposal", policy["policy"]["proposal"], None
                elif c.get("terms") and policy["policy"]["allow_accept"] and not c["acceptances"].get(store.local_urn, {}).get("active"):
                    kind, payload, reason = "accept", {}, None
                else:
                    reason = "waiting_for_peer"
                if kind:
                    operation_id = "worker-op-" + digest([context.task_id, context.policy_revision, c["collaboration_id"], kind, c.get("terms"), payload])[:48]
            if kind:
                prepared = store.prepare_collaboration(context.task_id, c["collaboration_id"], operation_id, kind, payload, context.owner_session)
                if prepared.get("decision") != "allow":
                    reason = "exception_requires_owner"
                    operation_id = None
            if operation_id:
                result = store.dispatch_collaboration(operation_id, context.owner_session, transport, worker_context=context)
                reason = None if result.get("status") == "accepted" else "send_uncertain" if result.get("status") == "sending" else "state_changed"
            with store._transaction():
                current = store._get("worker_policy", context.task_id)
                run = store._get("worker_run", context.run_id)
                run["status"] = "finished"
                store._put("worker_run", context.run_id, run)
                if current["policy_revision"] == context.policy_revision and current["status"] == "active":
                    current["waiting_reason"] = reason
                    if reason in {"exception_requires_owner", "send_uncertain", "state_changed", "collaboration_complete"}:
                        current["status"] = "paused"
                    store._put("worker_policy", context.task_id, current)
                    if reason != "waiting_for_peer":
                        store._worker_attention(current)
                results.append({"task_id": context.task_id, "operation_id": operation_id, "waiting_reason": reason})
        except Exception:
            if context:
                with store._transaction():
                    current = store._get("worker_policy", context.task_id)
                    run = store._get("worker_run", context.run_id)
                    if run:
                        run["status"] = "finished"
                        store._put("worker_run", context.run_id, run)
                    if current and current["policy_revision"] == context.policy_revision and current["status"] == "active":
                        current.update(status="paused", waiting_reason="worker_error")
                        store._put("worker_policy", context.task_id, current)
                        store._worker_attention(current)
            results.append({"task_id": saved["task_id"], "waiting_reason": "worker_error"})
    return {"schema": "agent-comm-worker-tick/v1", "results": results}
