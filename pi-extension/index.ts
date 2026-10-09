/**
 * claude-mnemonic for pi: the same memory store Claude Code and Claude Desktop use.
 *
 * Pi has no command hooks, so this extension runs claude-mnemonic's hook binaries on pi's lifecycle events, with the
 * input Claude Code would give them. The binaries keep the project ids, the worker start and the timeouts in one place,
 * so a folder is the same project in pi as in Claude Code. The binaries are installed in ~/.claude-mnemonic/bin by
 * `make install`, install.sh or the Claude Code plugin; without them this extension does nothing.
 *
 *   session_start          -> session-start   (saved context, injected with the first prompt)
 *   before_agent_start     -> user-prompt     (relevant notes for the prompt, starts the session in the worker)
 *   tool_result            -> post-tool-use   (observations)
 *   agent_settled          -> stop            (session summary)
 *   session_before_compact -> pre-compact     (summary of the conversation that compaction drops)
 *
 *   session_start, agent_settled and a timer -> statusline (the footer status; /memory-statusline turns it off or on)
 *
 * The MCP server gives the model the search, timeline and other memory tools.
 */

import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const DATA = join(homedir(), ".claude-mnemonic");
const BIN = join(DATA, "bin");
// This extension's own preferences (the worker watches settings.json and reloads on every change, so they live apart).
const PREFS = join(DATA, "pi.json");
const EXE = process.platform === "win32" ? ".exe" : "";

// Pi's built-in tools under the names the worker knows from Claude Code, so its filters apply to them too.
const TOOL_NAMES: Record<string, string> = {
	bash: "Bash",
	powershell: "Bash",
	read: "Read",
	edit: "Edit",
	write: "Write",
	grep: "Grep",
	find: "Glob",
	ls: "LS",
};

// How many recent turns the stop and pre-compact hooks get; they only use the end of the conversation.
const MAX_MESSAGES = 60;

// How often the footer status is refreshed while pi is idle; it is also refreshed after every prompt.
const STATUS_INTERVAL_MS = 15_000;

interface HookOutput {
	additionalContext?: string;
	systemMessage?: string;
}

interface Message {
	role: "user" | "assistant";
	text: string;
}

function binary(name: string): string | undefined {
	const path = join(BIN, name === "mcp-server" ? name : join("hooks", name)) + EXE;
	return existsSync(path) ? path : undefined;
}

/** Runs a binary with `input` on stdin and returns its stdout; any failure yields "". */
function runBinary(name: string, input: object, timeoutMs: number, env?: NodeJS.ProcessEnv): Promise<string> {
	const bin = binary(name);
	if (!bin) return Promise.resolve("");
	return new Promise((resolve) => {
		const child = spawn(bin, [], { stdio: ["pipe", "pipe", "ignore"], windowsHide: true, env: { ...process.env, ...env } });
		let out = "";
		const timer = setTimeout(() => child.kill(), timeoutMs);
		child.stdout.on("data", (chunk) => (out += chunk));
		child.on("error", () => resolve(""));
		child.on("close", () => {
			clearTimeout(timer);
			resolve(out);
		});
		child.stdin.on("error", () => {});
		child.stdin.end(JSON.stringify(input));
	});
}

/** Runs a hook binary and returns what it said; any failure yields {}. */
async function runHook(name: string, input: object, timeoutMs: number): Promise<HookOutput> {
	try {
		const parsed = JSON.parse((await runBinary(name, input, timeoutMs)).trim().split("\n").pop() || "{}");
		return {
			additionalContext: parsed?.hookSpecificOutput?.additionalContext,
			systemMessage: parsed?.systemMessage,
		};
	} catch {
		return {};
	}
}

/** Starts a hook that pi should not wait for (observations, summaries); it outlives pi if pi quits first. */
function fireHook(name: string, input: object): void {
	const bin = binary(name);
	if (!bin) return;
	try {
		const child = spawn(bin, [], { stdio: ["pipe", "ignore", "ignore"], detached: true, windowsHide: true });
		child.on("error", () => {});
		child.stdin.on("error", () => {});
		child.stdin.end(JSON.stringify(input));
		child.unref();
	} catch {
		// Memory is best effort; never disturb the session.
	}
}

function textOf(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.filter((block) => block?.type === "text" && typeof block.text === "string")
		.map((block) => block.text)
		.join("\n");
}

/** The user and assistant text turns of session entries, oldest first. */
function messagesOf(entries: readonly any[]): Message[] {
	const out: Message[] = [];
	for (const entry of entries) {
		const role = entry?.type === "message" ? entry.message?.role : undefined;
		if (role !== "user" && role !== "assistant") continue;
		const text = textOf(entry.message.content).trim();
		if (text) out.push({ role, text });
	}
	return out.slice(-MAX_MESSAGES);
}

function workerPort(): number {
	const fromEnv = Number(process.env.CLAUDE_MNEMONIC_WORKER_PORT);
	if (fromEnv) return fromEnv;
	try {
		const settings = JSON.parse(readFileSync(join(homedir(), ".claude-mnemonic", "settings.json"), "utf8"));
		const port = Number(settings?.CLAUDE_MNEMONIC_WORKER_PORT);
		if (port) return port;
	} catch {
		// No settings file: the default port.
	}
	return 37777;
}

interface Prefs {
	statusline?: boolean;
}

function readPrefs(): Prefs {
	try {
		return JSON.parse(readFileSync(PREFS, "utf8")) ?? {};
	} catch {
		return {};
	}
}

function writePrefs(prefs: Prefs): void {
	mkdirSync(DATA, { recursive: true });
	writeFileSync(PREFS, JSON.stringify(prefs, null, 2) + "\n");
}

/**
 * Extension files in pi's extension folders that set their own footer. Such a footer shows extension statuses only if it
 * renders them, so the status may not be visible. Pi cannot tell whether a footer is set, so this reads the sources; it
 * does not see footers from installed packages.
 */
function customFooters(cwd: string): string[] {
	const agentDir = process.env.PI_CODING_AGENT_DIR || join(homedir(), ".pi", "agent");
	const found: string[] = [];
	for (const dir of [join(agentDir, "extensions"), join(cwd, ".pi", "extensions")]) {
		let names: string[];
		try {
			names = readdirSync(dir);
		} catch {
			continue;
		}
		for (const name of names) {
			for (const file of [join(dir, name), join(dir, name, "index.ts"), join(dir, name, "index.js")]) {
				if (!/\.[jt]s$/.test(file)) continue;
				try {
					if (readFileSync(file, "utf8").includes("setFooter(")) found.push(file);
				} catch {
					// Not a file.
				}
			}
		}
	}
	return found;
}

export default function claudeMnemonic(pi: ExtensionAPI) {
	const mcpServer = binary("mcp-server");
	if (mcpServer) {
		// The server picks the project from its working directory, like Claude Code's plugin does. Its few tools are
		// declared to the model directly, as in Claude Code, rather than only reachable from codemode scripts.
		pi.registerMcpServer("claude-mnemonic", {
			command: mcpServer,
			exposure: "direct",
			cwd: process.cwd(),
			env: { CLAUDE_PROJECT_DIR: process.cwd() },
		});
	}

	// The saved context from session-start waits here until the first prompt, which carries it to the model.
	let pendingContext = "";
	let statusTimer: ReturnType<typeof setInterval> | undefined;

	// The footer status is the line Claude Code's status line shows, from the same binary
	// (CLAUDE_MNEMONIC_STATUSLINE_FORMAT picks default, compact or minimal). Pi's footer is not a terminal hyperlink target,
	// so the dashboard link is left out.
	const refreshStatus = async (ctx: ExtensionContext) => {
		if (!ctx.hasUI) return;
		const line = await runBinary(
			"statusline",
			{ session_id: `pi-${ctx.sessionManager.getSessionId()}`, cwd: ctx.cwd, workspace: { current_dir: ctx.cwd } },
			2_000,
			{ CLAUDE_MNEMONIC_STATUSLINE_LINK: "false" },
		);
		try {
			ctx.ui.setStatus("claude-mnemonic", line.trim() || undefined);
		} catch {
			// The session ended while the binary ran; its context is stale.
		}
	};

	const base = (ctx: ExtensionContext, event: string) => ({
		session_id: `pi-${ctx.sessionManager.getSessionId()}`,
		cwd: ctx.cwd,
		hook_event_name: event,
	});

	const startStatus = (ctx: ExtensionContext) => {
		stopStatus();
		if (!ctx.hasUI || !binary("statusline")) return;
		void refreshStatus(ctx);
		statusTimer = setInterval(() => void refreshStatus(ctx), STATUS_INTERVAL_MS);
		statusTimer.unref?.();
	};

	const stopStatus = () => {
		clearInterval(statusTimer);
		statusTimer = undefined;
	};

	pi.on("session_start", async (event, ctx) => {
		if (!binary("session-start")) {
			if (ctx.hasUI) ctx.ui.notify("claude-mnemonic: no binaries in ~/.claude-mnemonic/bin, memory is off", "warning");
			return;
		}
		const source = { startup: "startup", new: "clear", resume: "resume", fork: "resume", reload: "resume" }[event.reason];
		const out = await runHook("session-start", { ...base(ctx, "SessionStart"), source }, 30_000);
		pendingContext = out.additionalContext ?? "";
		if (out.systemMessage && ctx.hasUI) ctx.ui.notify(out.systemMessage, "info");
		if (readPrefs().statusline !== false) startStatus(ctx);
	});

	pi.on("session_shutdown", async () => stopStatus());

	pi.on("before_agent_start", async (event, ctx) => {
		const out = await runHook("user-prompt", { ...base(ctx, "UserPromptSubmit"), prompt: event.prompt }, 10_000);
		const content = [pendingContext, out.additionalContext].filter(Boolean).join("\n\n");
		pendingContext = "";
		if (!content) return;
		return { message: { customType: "claude-mnemonic", content, display: false } };
	});

	pi.on("tool_result", async (event, ctx) => {
		if (event.parentToolCallId) return; // nested calls are reported by the tool that made them
		fireHook("post-tool-use", {
			...base(ctx, "PostToolUse"),
			tool_name: TOOL_NAMES[event.toolName] ?? event.toolName,
			tool_input: event.input,
			tool_response: { output: textOf(event.content), isError: event.isError },
			tool_use_id: event.toolCallId,
		});
	});

	pi.on("agent_settled", async (event, ctx) => {
		if (statusTimer) void refreshStatus(ctx);
		if (event.aborted) return;
		const messages = messagesOf(ctx.sessionManager.getBranch());
		if (messages.length > 0) fireHook("stop", { ...base(ctx, "Stop"), messages });
	});

	pi.on("session_before_compact", async (event, ctx) => {
		const messages = messagesOf(event.branchEntries);
		if (messages.length === 0) return;
		const trigger = event.reason === "manual" ? "manual" : "auto";
		await runHook("pre-compact", { ...base(ctx, "PreCompact"), trigger, messages }, 30_000);
	});

	pi.registerCommand("memory-dashboard", {
		description: "Show the address of the claude-mnemonic dashboard",
		handler: async (_args, ctx) => {
			const url = `http://localhost:${workerPort()}`;
			const running = await fetch(`${url}/health`).then((r) => r.ok, () => false);
			ctx.ui.notify(running ? `claude-mnemonic dashboard: ${url}` : `claude-mnemonic worker is not running (${url})`, running ? "info" : "warning");
		},
	});

	// The same command as in Claude Code. There it writes Claude Code's settings.json, because a plugin cannot set the
	// status line; here the extension sets it itself, so the command only turns it on or off.
	pi.registerCommand("memory-statusline", {
		description: "Show or hide the claude-mnemonic status in the footer: on (default), off, status",
		getArgumentCompletions: (prefix) =>
			["on", "off", "status"].filter((a) => a.startsWith(prefix.trim())).map((a) => ({ value: a, label: a })),
		handler: async (args, ctx) => {
			const action = args.trim() || "on";
			if (!["on", "off", "status"].includes(action)) {
				ctx.ui.notify("Usage: /memory-statusline [on|off|status]", "warning");
				return;
			}
			if (!binary("statusline")) {
				ctx.ui.notify("claude-mnemonic: the status line binary is not installed in ~/.claude-mnemonic/bin", "warning");
				return;
			}
			if (action === "on") {
				writePrefs({ ...readPrefs(), statusline: true });
				startStatus(ctx);
			} else if (action === "off") {
				writePrefs({ ...readPrefs(), statusline: false });
				stopStatus();
				ctx.ui.setStatus("claude-mnemonic", undefined);
			}
			const on = statusTimer !== undefined;
			const lines = [`claude-mnemonic status line: ${on ? "on" : "off"}`];
			const footers = on ? customFooters(ctx.cwd) : [];
			if (footers.length > 0) {
				lines.push(
					`A custom footer is set by ${footers.join(", ")}. It shows the status only if it renders the extension ` +
						`statuses (footerData.getExtensionStatuses(), key "claude-mnemonic").`,
				);
			}
			ctx.ui.notify(lines.join("\n"), footers.length > 0 ? "warning" : "info");
		},
	});
}
