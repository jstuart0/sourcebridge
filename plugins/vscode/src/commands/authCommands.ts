// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Auth commands: sign-in (OIDC or local password), sign-out, and the
 * first-run "configure server URL + paste token" shortcut.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { classifyError } from "./common";
import * as log from "../logging";

export function registerAuthCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.signIn", async () => {
      log.info("command", "signIn invoked");
      const config = vscode.workspace.getConfiguration("sourcebridge");
      const currentUrl = config.get<string>("apiUrl", "http://localhost:8080");
      const url = await vscode.window.showInputBox({
        prompt: "SourceBridge server URL",
        value: currentUrl,
        placeHolder: "http://localhost:8080",
      });
      if (!url) return;
      await config.update("apiUrl", url, vscode.ConfigurationTarget.Workspace);
      await client.reloadConfiguration();

      try {
        const authInfo = await client.getDesktopAuthInfo();
        let token = "";

        if (authInfo.oidc_enabled) {
          const choice = await vscode.window.showQuickPick(
            [
              { label: "Sign In With Browser", mode: "oidc" },
              ...(authInfo.local_auth && authInfo.setup_done
                ? [{ label: "Sign In With Password", mode: "local" as const }]
                : []),
            ],
            { placeHolder: "Choose a sign-in method" },
          );
          if (!choice) return;
          if (choice.mode === "oidc") {
            const session = await client.startDesktopOIDC();
            await vscode.env.openExternal(vscode.Uri.parse(session.auth_url));
            token = await vscode.window.withProgress(
              {
                location: vscode.ProgressLocation.Notification,
                title: "SourceBridge: waiting for browser sign-in...",
                cancellable: false,
              },
              async (progress) => {
                const started = Date.now();
                while (Date.now() - started < session.expires_in * 1000) {
                  progress.report({ message: "Complete sign-in in your browser" });
                  const poll = await client.pollDesktopOIDC(session.session_id);
                  if (poll.status === "complete" && poll.token) return poll.token;
                  await new Promise((r) => setTimeout(r, 2000));
                }
                throw new Error("browser sign-in timed out");
              },
            );
          } else {
            const password = await vscode.window.showInputBox({
              prompt: "SourceBridge password",
              password: true,
            });
            if (!password) return;
            token = await client.desktopLocalLogin(password, "VS Code");
          }
        } else if (authInfo.local_auth && authInfo.setup_done) {
          const password = await vscode.window.showInputBox({
            prompt: "SourceBridge password",
            password: true,
          });
          if (!password) return;
          token = await client.desktopLocalLogin(password, "VS Code");
        } else {
          vscode.window.showWarningMessage(
            "This server does not expose a desktop sign-in flow yet.",
          );
          return;
        }

        await client.storeToken(token);
        vscode.window.showInformationMessage(`Signed in to SourceBridge at ${url}`);
      } catch (err) {
        vscode.window.showErrorMessage(`Sign-in failed: ${classifyError(err)}`);
      }
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.signOut", async () => {
      log.info("command", "signOut invoked");
      try {
        await client.revokeCurrentToken();
      } catch (err) {
        vscode.window.showWarningMessage(
          `SourceBridge sign-out could not revoke the current session: ${classifyError(err)}`,
        );
      }
      await client.clearStoredToken();
      vscode.window.showInformationMessage("Signed out of SourceBridge.");
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.configure", async () => {
      log.info("command", "configure invoked");
      const config = vscode.workspace.getConfiguration("sourcebridge");
      const currentUrl = config.get<string>("apiUrl", "http://localhost:8080");
      const url = await vscode.window.showInputBox({
        prompt: "SourceBridge server URL",
        value: currentUrl,
        placeHolder: "http://localhost:8080",
      });
      if (url === undefined) return;
      const token = await vscode.window.showInputBox({
        prompt: "Authentication token (leave empty for no auth)",
        password: true,
        placeHolder: "paste your JWT token",
      });
      if (token === undefined) return;
      await config.update("apiUrl", url, vscode.ConfigurationTarget.Workspace);
      await client.storeToken(token);
      const testClient = new SourceBridgeClient(context);
      if (await testClient.isServerRunning()) {
        vscode.window.showInformationMessage(`Connected to SourceBridge server at ${url}`);
      } else {
        vscode.window.showWarningMessage(
          `Saved settings but could not reach server at ${url}. Make sure the server is running.`,
        );
      }
    }),
  );
}
