import CssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker';
import HtmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker';
import JsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';
import TsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';

export interface FrontendEditorController {
  setDocument(value: string, language: string): void;
  focus(): void;
  destroy(): void;
}

declare global {
  interface Window {
    MonacoEnvironment?: {
      getWorker?: (_workerId: string, label: string) => Worker;
    };
  }
}

function ensureMonacoWorkers(): void {
  if (window.MonacoEnvironment?.getWorker) {
    return;
  }

  window.MonacoEnvironment = {
    getWorker(_workerId: string, label: string): Worker {
      switch (label) {
        case 'json':
          return new JsonWorker();
        case 'css':
        case 'scss':
        case 'less':
          return new CssWorker();
        case 'html':
        case 'handlebars':
        case 'razor':
          return new HtmlWorker();
        case 'typescript':
        case 'javascript':
          return new TsWorker();
        default:
          return new EditorWorker();
      }
    },
  };
}

export async function createBrowserEditorController(
  host: HTMLElement,
  onChange: (value: string) => void,
): Promise<FrontendEditorController> {
  host.innerHTML = '';

  try {
    ensureMonacoWorkers();
    const monaco = await import('monaco-editor');
    let silent = false;
    const editor = monaco.editor.create(host, {
      value: '',
      language: 'plaintext',
      automaticLayout: true,
      minimap: { enabled: false },
      roundedSelection: false,
      scrollBeyondLastLine: false,
      fontFamily: 'IBM Plex Mono, SFMono-Regular, monospace',
      fontLigatures: false,
      fontSize: 14,
      theme: 'vs',
    });
    editor.onDidChangeModelContent(() => {
      if (!silent) {
        onChange(editor.getValue());
      }
    });

    return {
      setDocument(value: string, language: string): void {
        silent = true;
        try {
          editor.setValue(value);
          const model = editor.getModel();
          if (model) {
            monaco.editor.setModelLanguage(model, language || 'plaintext');
          }
        } finally {
          silent = false;
        }
      },
      focus(): void {
        editor.focus();
      },
      destroy(): void {
        editor.dispose();
      },
    };
  } catch {
    const textarea = document.createElement('textarea');
    textarea.className = 'sdn-frontend-editor__fallback';
    textarea.spellcheck = false;
    textarea.addEventListener('input', () => {
      onChange(textarea.value);
    });
    host.replaceChildren(textarea);

    return {
      setDocument(value: string): void {
        textarea.value = value;
      },
      focus(): void {
        textarea.focus();
      },
      destroy(): void {
        textarea.remove();
      },
    };
  }
}
