#!/usr/bin/env node

const fs = require("fs");
const path = require("path");
const os = require("os");
const { execSync, execFileSync } = require("child_process");

console.log("=== Agent Comm unified connector setup ===");

const cwd = process.cwd();

// Detect framework
const isOpenClaw = fs.existsSync(path.join(cwd, "package.json"));
const isHermes = fs.existsSync(path.join(cwd, "config.yaml")) || fs.existsSync(path.join(cwd, "pyproject.toml"));

if (!isOpenClaw && !isHermes) {
  console.error("Error: Could not detect OpenClaw or Hermes framework in the current directory.");
  console.error("Please run this command from the root of your Agent project.");
  process.exit(1);
}

const framework = isOpenClaw ? "OpenClaw" : "Hermes";
console.log(`Detected Framework: ${framework}`);

// Paths to local packages in our repo
const openclawChannelPath = path.resolve(__dirname, "../../openclaw-channel");
const hermesPlatformPath = path.resolve(__dirname, "../../hermes-platform");
const goSDKPath = path.resolve(__dirname, "../../../");

// Default public Platform endpoint. Override with AGENT_PLATFORM_URL env var.
const DEFAULT_PLATFORM_URL =
  process.env.AGENT_PLATFORM_URL || "https://agent-communication.online";

const https = require("https");
const http = require("http");

function fetchJson(url) {
  return new Promise((resolve, reject) => {
    const req = https.get(url, { headers: { "User-Agent": "agent-comm-cli" }, timeout: 5000 }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        fetchJson(res.headers.location).then(resolve).catch(reject);
        return;
      }
      if (res.statusCode !== 200) {
        reject(new Error(`Server returned status code ${res.statusCode}`));
        return;
      }
      let body = "";
      res.on("data", chunk => { body += chunk; });
      res.on("end", () => {
        try { resolve(JSON.parse(body)); }
        catch (e) { reject(e); }
      });
    });
    req.on("timeout", () => {
      req.destroy();
      reject(new Error("Connection timeout"));
    });
    req.on("error", reject);
  });
}

function downloadFile(url, dest) {
  return new Promise((resolve, reject) => {
    const file = fs.createWriteStream(dest);
    const req = https.get(url, { headers: { "User-Agent": "agent-comm-cli" }, timeout: 5000 }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        file.close();
        try { fs.unlinkSync(dest); } catch(e) {}
        downloadFile(res.headers.location, dest).then(resolve).catch(reject);
        return;
      }
      if (res.statusCode !== 200) {
        file.close();
        try { fs.unlinkSync(dest); } catch(e) {}
        reject(new Error(`Server returned status code ${res.statusCode}`));
        return;
      }
      res.pipe(file);
      file.on("finish", () => {
        file.close();
        resolve();
      });
    });
    req.on("timeout", () => {
      req.destroy();
      file.close();
      try { fs.unlinkSync(dest); } catch(e) {}
      reject(new Error("Connection timeout"));
    });
    req.on("error", (err) => {
      file.close();
      try { fs.unlinkSync(dest); } catch(e) {}
      reject(err);
    });
  });
}

function postJson(url, headers, bodyObj) {
  return new Promise((resolve, reject) => {
    const parsedUrl = new URL(url);
    const client = parsedUrl.protocol === "https:" ? https : http;
    const bodyStr = JSON.stringify(bodyObj);
    const bodyBytes = Buffer.from(bodyStr, "utf8");

    const req = client.request({
      hostname: parsedUrl.hostname,
      port: parsedUrl.port || (parsedUrl.protocol === "https:" ? 443 : 80),
      path: parsedUrl.pathname + parsedUrl.search,
      method: "POST",
      headers: {
        ...headers,
        "Content-Type": "application/json",
        "Content-Length": bodyBytes.length,
        "User-Agent": "agent-comm-cli"
      },
      timeout: 5000
    }, (res) => {
      let responseBody = "";
      res.on("data", chunk => { responseBody += chunk; });
      res.on("end", () => {
        if (res.statusCode >= 200 && res.statusCode < 300) {
          try { resolve(JSON.parse(responseBody)); }
          catch (e) { resolve(responseBody); }
        } else {
          reject(new Error(`Server returned status code ${res.statusCode}: ${responseBody}`));
        }
      });
    });

    req.on("timeout", () => {
      req.destroy();
      reject(new Error("Connection timeout"));
    });
    req.on("error", reject);
    req.write(bodyBytes);
    req.end();
  });
}

async function installGoHelper() {
  const osType = process.platform === "win32" ? "windows" : (process.platform === "darwin" ? "darwin" : "linux");
  const archType = process.arch === "x64" ? "amd64" : (process.arch === "arm64" ? "arm64" : "amd64");
  const targetAssetName = `agent-comm-helper-${osType}-${archType}${osType === "windows" ? ".exe" : ""}`;

  console.log(`\n1. Acquiring Go Helper tool...`);
  console.log(`Checking for precompiled binary: ${targetAssetName} from GitHub Releases...`);

  const binDir = path.join(os.homedir(), ".agent-comm", "bin");
  fs.mkdirSync(binDir, { recursive: true });
  const destPath = path.join(binDir, osType === "windows" ? "agent-comm-helper.exe" : "agent-comm-helper");

  try {
    const releaseData = await fetchJson("https://api.github.com/repos/BillShiyaoZhang/agent-comm/releases/latest");
    const asset = releaseData.assets.find(a => a.name === targetAssetName);
    if (!asset) {
      throw new Error(`Asset ${targetAssetName} not found in the latest release.`);
    }

    console.log(`Downloading helper from: ${asset.browser_download_url}`);
    await downloadFile(asset.browser_download_url, destPath);
    try {
      fs.chmodSync(destPath, "755");
    } catch (e) {}
    console.log("Precompiled helper installed successfully.");
    return;
  } catch (err) {
    console.warn(`Could not download precompiled helper (${err.message}).`);
    console.log("Falling back to local Go compilation...");
  }

  // Fallback compile
  try {
    if (process.platform === "win32") {
      console.log("Running compilation inside WSL...");
      execSync(`wsl mkdir -p ~/.agent-comm/bin`, { stdio: "inherit" });
      execSync(`wsl go build -o ~/.agent-comm/bin/agent-comm-helper ./cmd/helper`, {
        cwd: goSDKPath,
        stdio: "inherit"
      });
    } else {
      console.log("Running compilation natively...");
      execSync(`mkdir -p ~/.agent-comm/bin`, { stdio: "inherit" });
      execSync(`go build -o ~/.agent-comm/bin/agent-comm-helper ./cmd/helper`, {
        cwd: goSDKPath,
        stdio: "inherit"
      });
    }
    console.log("Go Helper compiled successfully from source.");
  } catch (err) {
    console.error(`Local compilation failed: ${err.message}`);
    process.exit(1);
  }
}

// Invoke helper installation
(async () => {
  await installGoHelper();
  
  // Continue setup after async helper installation
  await continueSetup();
})();

async function continueSetup() {

// 2. Install package dependency
console.log(`\n2. Installing ${framework} channel connector adapter...`);
try {
  if (isOpenClaw) {
    console.log(`Running: npm install ${openclawChannelPath}`);
    execSync(`npm install ${openclawChannelPath}`, { stdio: "inherit", cwd });
  } else {
    // Hermes: python package setup
    // Try uv first, then fallback to pip
    let hasUv = false;
    try {
      execSync("uv --version", { stdio: "ignore" });
      hasUv = true;
    } catch (e) {}

    if (hasUv) {
      console.log(`Running: uv pip install -e ${hermesPlatformPath}`);
      execSync(`uv pip install -e ${hermesPlatformPath}`, { stdio: "inherit", cwd });
    } else {
      console.log(`Running: pip install -e ${hermesPlatformPath}`);
      execSync(`pip install -e ${hermesPlatformPath}`, { stdio: "inherit", cwd });
    }
  }
  console.log("Channel adapter installed successfully.");
} catch (err) {
  console.error(`Installation failed: ${err.message}`);
  process.exit(1);
}

// 3. Generate URN and Keys. We default all frameworks to a single
// framework-agnostic directory under the user's home so the same identity
// can be reused across frameworks. Operators can still pass --keys-dir to
// use a custom path.
console.log("\n3. Generating secure keys & identity URN...");
let keysDir = process.env.AGENT_COMM_KEYS_DIR;
if (!keysDir) {
  // Process CLI flag overrides (e.g. "node cli.js init --keys-dir <path>")
  const cliKeysIdx = process.argv.indexOf("--keys-dir");
  if (cliKeysIdx >= 0 && process.argv[cliKeysIdx + 1]) {
    keysDir = process.argv[cliKeysIdx + 1];
  }
}
if (!keysDir) {
  keysDir = path.join(os.homedir(), ".agent-comm", "keys");
}
keysDir = path.resolve(keysDir);

fs.mkdirSync(keysDir, { recursive: true });

let initRes;
try {
  if (process.platform === "win32") {
    const wslKeysDir = execSync(`wsl wslpath '${keysDir.replace(/\\/g, "/")}'`).toString().trim();
    const helperOutput = execFileSync("wsl", ["~/.agent-comm/bin/agent-comm-helper", "init", wslKeysDir]).toString().trim();
    initRes = JSON.parse(helperOutput);
  } else {
    const home = os.homedir();
    const helperPath = path.join(home, ".agent-comm", "bin", "agent-comm-helper");
    const helperOutput = execFileSync(helperPath, ["init", keysDir]).toString().trim();
    initRes = JSON.parse(helperOutput);
  }

  if (initRes.error) {
    throw new Error(initRes.error);
  }

  console.log(`URN generated: ${initRes.urn}`);
  console.log(`Peer ID derived: ${initRes.peer_id}`);
  console.log(`Keys stored in: ${keysDir}`);
} catch (err) {
  console.error(`Key generation failed: ${err.message}`);
  process.exit(1);
}

// 3.5 [Removed] URN registration is now handled automatically by the Go Helper Daemon on startup.

// 4. Update configuration file
console.log("\n4. Injecting configuration into framework settings...");
try {
  if (isOpenClaw) {
    const settingsPath = path.join(cwd, "settings.json");
    let settings = {};
    if (fs.existsSync(settingsPath)) {
      try {
        settings = JSON.parse(fs.readFileSync(settingsPath, "utf8"));
      } catch (e) {
        console.warn("Warning: Could not parse existing settings.json, creating a new one.");
      }
    }

    if (!settings.channels) settings.channels = {};
    settings.channels["agent-comm"] = {
      enabled: true,
      platform_url: "http://127.0.0.1:45042/api/v1/mq",
      urn: initRes.urn,
      // Use the resolved absolute path so frameworks that do not perform
      // their own ~ expansion still locate the keys directory.
      keys_dir: keysDir,
    };

    fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2), "utf8");
    console.log(`Successfully updated ${settingsPath}`);
  } else {
    // Hermes: config.yaml
    const configPath = path.join(cwd, "config.yaml");
    let content = "";
    if (fs.existsSync(configPath)) {
      content = fs.readFileSync(configPath, "utf8");
    }

    // Use the resolved absolute keys directory; this is what we just generated
// keys into, so the runtime can read them back without any ~ expansion.
const configBlock = `
platforms:
  agent_comm:
    enabled: true
    platform_url: "http://127.0.0.1:45042"
    urn: "${initRes.urn}"
    keys_path: "${keysDir}"
`;

    // Simple robust yaml patch
    if (content.includes("platforms:")) {
      if (content.includes("agent_comm:")) {
        console.log("Configuration for agent_comm already exists in config.yaml. Skipping YAML write.");
      } else {
        content = content.replace("platforms:", "platforms:\n  agent_comm:\n    enabled: true\n    platform_url: \"http://127.0.0.1:45042\"\n    urn: \"" + initRes.urn + "\"\n    keys_path: \"" + keysDir + "\"");
        fs.writeFileSync(configPath, content, "utf8");
        console.log(`Successfully patched ${configPath}`);
      }
    } else {
      content += configBlock;
      fs.writeFileSync(configPath, content, "utf8");
      console.log(`Successfully created/updated ${configPath}`);
    }
  }
} catch (err) {
  console.error(`Configuration update failed: ${err.message}`);
  process.exit(1);
}

console.log("\nSetup completed successfully! Your Agent Comm connector is configured.");
console.log("\n=== 💡 HOW TO START THE LOCAL P2P DAEMON ===");
console.log("To enable P2P secure communications, please start the background daemon:");
if (process.platform === "win32") {
  const wslKeysDir = execSync(`wsl wslpath '${keysDir.replace(/\\/g, "/")}'`).toString().trim();
  console.log(`  wsl ~/.agent-comm/bin/agent-comm-helper daemon ${wslKeysDir} ${DEFAULT_PLATFORM_URL}`);
} else {
  console.log(`  ~/.agent-comm/bin/agent-comm-helper daemon ${keysDir} ${DEFAULT_PLATFORM_URL}`);
}
console.log("============================================\n");
}
