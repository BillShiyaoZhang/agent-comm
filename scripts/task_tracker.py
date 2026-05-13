#!/usr/bin/env python3
"""Task delegation tracker for agent-comm.

CLI subcommands:
  create       Create a new task delegation
  list-active  List all active task IDs
  read         Read task content
  check        Check if action is in the task list
  mark-done    Mark a subtask as completed
  append-log   Add an entry to the operation log
  archive      Archive a completed task
"""

import argparse
import json
import os
import sys
import uuid
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(__file__))
from paths import CONTACTS_DIR

TASKS_DIR = os.path.join(CONTACTS_DIR, "tasks")
ARCHIVED_DIR = os.path.join(TASKS_DIR, "archived")

# ISO 8601 timestamp format
TS_FMT = "%Y-%m-%dT%H:%M:%SZ"


def _ensure_dirs():
    os.makedirs(TASKS_DIR, exist_ok=True)
    os.makedirs(ARCHIVED_DIR, exist_ok=True)


def _task_path(task_id: str) -> str:
    return os.path.join(TASKS_DIR, f"{task_id}.md")


def _archived_path(task_id: str) -> str:
    return os.path.join(ARCHIVED_DIR, f"{task_id}.md")


def _iso_now() -> str:
    return datetime.now(timezone.utc).strftime(TS_FMT)


def cmd_create(args: argparse.Namespace) -> int:
    _ensure_dirs()
    task_id = str(uuid.uuid4())
    now = _iso_now()
    content = (
        f"# Task Delegation: {task_id}\n"
        f"\n"
        f"**委托方**: {args.peer_id}\n"
        f"**创建时间**: {now}\n"
        f"**状态**: in_progress\n"
        f"\n"
        f"## 任务\n"
        f"\n"
        f"- [] {args.description}\n"
        f"\n"
        f"## 备注\n"
        f"\n"
        f"<!-- agent 在此记录关键决策 -->\n"
        f"\n"
        f"## 操作日志\n"
        f"\n"
        f"[{now}] 任务已创建\n"
    )
    path = _task_path(task_id)
    with open(path, "w") as f:
        f.write(content)
    print(task_id)
    return 0


def cmd_list_active(args: argparse.Namespace) -> int:
    _ensure_dirs()
    if not os.path.isdir(TASKS_DIR):
        return 0
    files = sorted(
        f.replace(".md", "")
        for f in os.listdir(TASKS_DIR)
        if f.endswith(".md")
    )
    for task_id in files:
        print(task_id)
    return 0


def cmd_read(args: argparse.Namespace) -> int:
    path = _task_path(args.task_id)
    if not os.path.exists(path):
        print(f"ERROR: Task '{args.task_id}' not found", file=sys.stderr)
        return 1
    with open(path) as f:
        print(f.read(), end="")
    return 0


def _find_task_or_archived(task_id: str) -> str | None:
    """Return path to task file, checking active first then archived."""
    for path in (_task_path(task_id), _archived_path(task_id)):
        if os.path.exists(path):
            return path
    return None


def cmd_check(args: argparse.Namespace) -> int:
    path = _find_task_or_archived(args.task_id)
    if not path:
        print(f"ERROR: Task '{args.task_id}' not found", file=sys.stderr)
        return 1
    with open(path) as f:
        content = f.read()
    needle = args.action_text.strip().lower()
    in_task_section = False
    for line in content.splitlines():
        if line.startswith("## 任务"):
            in_task_section = True
            continue
        if line.startswith("## "):
            in_task_section = False
            continue
        if in_task_section:
            stripped = line.strip()
            if stripped.startswith("- [") and needle in stripped.lower():
                print("yes")
                return 0
    print("no")
    return 0


def cmd_mark_done(args: argparse.Namespace) -> int:
    path = _task_path(args.task_id)
    if not os.path.exists(path):
        print(f"ERROR: Active task '{args.task_id}' not found", file=sys.stderr)
        return 1
    with open(path) as f:
        content = f.read()
    needle = args.subtask_text.strip()
    lines = content.splitlines(keepends=True)
    in_task_section = False
    replaced = False
    for i, line in enumerate(lines):
        if line.startswith("## 任务"):
            in_task_section = True
            continue
        if line.startswith("## "):
            in_task_section = False
            continue
        if in_task_section:
            stripped = line.strip()
            if stripped.startswith("- []") and needle in stripped:
                indent = line[: len(line) - len(line.lstrip())]
                lines[i] = f"{indent}- [x] {stripped[4:]}\n"
                replaced = True
                break
    if not replaced:
        print(f"ERROR: Subtask '{needle}' not found or already done", file=sys.stderr)
        return 1
    with open(path, "w") as f:
        f.writelines(lines)
    return 0


def cmd_append_log(args: argparse.Namespace) -> int:
    path = _task_path(args.task_id)
    if not os.path.exists(path):
        print(f"ERROR: Active task '{args.task_id}' not found", file=sys.stderr)
        return 1
    with open(path) as f:
        content = f.read()
    now = _iso_now()
    new_entry = f"[{now}] {args.entry}\n"
    if content.endswith("\n"):
        content += new_entry
    else:
        content += "\n" + new_entry
    with open(path, "w") as f:
        f.write(content)
    return 0


def cmd_archive(args: argparse.Namespace) -> int:
    src = _task_path(args.task_id)
    if not os.path.exists(src):
        print(f"ERROR: Active task '{args.task_id}' not found", file=sys.stderr)
        return 1
    # Read, update status, write to archived location
    with open(src) as f:
        content = f.read()
    content = content.replace("**状态**: in_progress", "**状态**: completed")
    dst = _archived_path(args.task_id)
    with open(dst, "w") as f:
        f.write(content)
    os.remove(src)
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Task delegation tracker")
    sub = parser.add_subparsers(dest="command", required=True)

    # create
    p_create = sub.add_parser("create", help="Create a new task delegation")
    p_create.add_argument("--peer-id", required=True)
    p_create.add_argument("--description", required=True)

    # list-active
    sub.add_parser("list-active", help="List all active task IDs")

    # read
    p_read = sub.add_parser("read", help="Read task content")
    p_read.add_argument("task_id")

    # check
    p_check = sub.add_parser("check", help="Check if action is in the task list")
    p_check.add_argument("task_id")
    p_check.add_argument("action_text")

    # mark-done
    p_done = sub.add_parser("mark-done", help="Mark a subtask as completed")
    p_done.add_argument("task_id")
    p_done.add_argument("subtask_text")

    # append-log
    p_log = sub.add_parser("append-log", help="Add entry to operation log")
    p_log.add_argument("task_id")
    p_log.add_argument("entry")

    # archive
    p_arch = sub.add_parser("archive", help="Archive a completed task")
    p_arch.add_argument("task_id")

    args = parser.parse_args()

    match args.command:
        case "create":
            return cmd_create(args)
        case "list-active":
            return cmd_list_active(args)
        case "read":
            return cmd_read(args)
        case "check":
            return cmd_check(args)
        case "mark-done":
            return cmd_mark_done(args)
        case "append-log":
            return cmd_append_log(args)
        case "archive":
            return cmd_archive(args)
        case _:
            parser.print_help()
            return 1


if __name__ == "__main__":
    sys.exit(main())
