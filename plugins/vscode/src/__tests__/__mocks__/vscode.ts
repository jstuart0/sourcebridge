export class Uri {
  static parse(value: string) {
    return { scheme: "file", path: value, toString: () => value };
  }
  static file(path: string) {
    return { scheme: "file", path, fsPath: path, toString: () => `file://${path}` };
  }
}

export class Position {
  constructor(public line: number, public character: number) {}
}

export class Range {
  constructor(public start: Position, public end: Position) {}
}

export class Location {
  constructor(public uri: any, public range: Range) {}
}

export class CodeLens {
  constructor(public range: Range, public command?: any) {}
}

export class Hover {
  constructor(public contents: any, public range?: Range) {}
}

export class MarkdownString {
  value: string;
  isTrusted: boolean;
  constructor(value?: string) {
    this.value = value || "";
    this.isTrusted = false;
  }
  appendMarkdown(value: string) {
    this.value += value;
    return this;
  }
  appendText(value: string) {
    this.value += value;
    return this;
  }
}

export enum TreeItemCollapsibleState {
  None = 0,
  Collapsed = 1,
  Expanded = 2,
}

export class TreeItem {
  label?: string;
  collapsibleState?: TreeItemCollapsibleState;
  constructor(label: string, collapsibleState?: TreeItemCollapsibleState) {
    this.label = label;
    this.collapsibleState = collapsibleState;
  }
}

export class EventEmitter {
  private _listeners: any[] = [];
  event = (listener: any) => {
    this._listeners.push(listener);
    return { dispose: () => {} };
  };
  fire(data?: any) {
    this._listeners.forEach((l) => l(data));
  }
  dispose() {}
}

export class Disposable {
  static from(...disposables: any[]) {
    return { dispose: () => disposables.forEach((d) => d.dispose?.()) };
  }
}

export enum ViewColumn {
  One = 1,
  Two = 2,
}

const registeredCommands = new Map<string, (...args: any[]) => any>();

export const commands = {
  registerCommand: (id: string, handler: (...args: any[]) => any) => {
    registeredCommands.set(id, handler);
    return { dispose: () => registeredCommands.delete(id) };
  },
  executeCommand: async (id: string, ...args: any[]) => {
    const handler = registeredCommands.get(id);
    if (handler) return handler(...args);
  },
  getCommands: async () => [...registeredCommands.keys()],
};

export enum ProgressLocation {
  SourceControl = 1,
  Window = 10,
  Notification = 15,
}

export const window = {
  showInformationMessage: jest.fn().mockResolvedValue(undefined),
  showErrorMessage: jest.fn().mockResolvedValue(undefined),
  showWarningMessage: jest.fn().mockResolvedValue(undefined),
  showInputBox: jest.fn().mockResolvedValue(undefined),
  showQuickPick: jest.fn().mockResolvedValue(undefined),
  withProgress: jest.fn().mockImplementation((_opts: any, task: any) => task({ report: jest.fn() })),
  createOutputChannel: () => ({
    appendLine: jest.fn(),
    append: jest.fn(),
    show: jest.fn(),
    dispose: jest.fn(),
  }),
  createWebviewPanel: jest.fn().mockReturnValue({
    webview: {
      html: "",
      onDidReceiveMessage: jest.fn(),
      postMessage: jest.fn(),
    },
    onDidDispose: jest.fn(),
    reveal: jest.fn(),
    dispose: jest.fn(),
  }),
  registerTreeDataProvider: jest.fn(),
  activeTextEditor: undefined as any,
  showTextDocument: jest.fn(),
  createTreeView: jest.fn().mockReturnValue({ dispose: jest.fn() }),
};

export const workspace = {
  getConfiguration: (section?: string) => ({
    get: (key: string, defaultValue?: any) => {
      if (section === "sourcebridge") {
        if (key === "apiUrl") return "http://localhost:8080";
        if (key === "token") return "";
      }
      return defaultValue;
    },
    update: jest.fn(),
  }),
  workspaceFolders: [{ uri: Uri.file("/workspace"), name: "test", index: 0 }],
  getWorkspaceFolder: jest.fn().mockReturnValue({
    uri: Uri.file("/workspace"),
    name: "test",
    index: 0,
  }),
  onDidChangeConfiguration: jest.fn(),
};

export const languages = {
  registerCodeLensProvider: jest.fn().mockReturnValue({ dispose: () => {} }),
  registerHoverProvider: jest.fn().mockReturnValue({ dispose: () => {} }),
};

export const extensions = {
  getExtension: jest.fn(),
};

export class ThemeColor {
  constructor(public id: string) {}
}

export class ThemeIcon {
  constructor(public id: string) {}
}

const decorationType = {
  key: "mock-decoration",
  dispose: jest.fn(),
};

export const window_createTextEditorDecorationType = jest.fn().mockReturnValue(decorationType);
Object.assign(window, { createTextEditorDecorationType: window_createTextEditorDecorationType });
