import { useState, type KeyboardEvent } from 'react';
import type { HarnessClient } from '@harness/protocol';

export function PromptInput({ client, disabled }: { client: HarnessClient; disabled: boolean }) {
  const [text, setText] = useState('');

  const send = () => {
    const value = text.trim();
    if (!value) return;
    setText('');
    void client.sendPrompt(value).catch(() => {
      /* offline queue and errors are reflected in connection state */
    });
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  return (
    <div className="composer">
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Send a prompt… (Enter to send, Shift+Enter for newline)"
        aria-label="Prompt"
        rows={2}
      />
      <button onClick={send} disabled={disabled || text.trim() === ''}>
        Send
      </button>
    </div>
  );
}
