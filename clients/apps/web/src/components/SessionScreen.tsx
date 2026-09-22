import type { ClientState, HarnessClient } from '@harness/protocol';
import { ConnectionBadge } from './ConnectionBadge';
import { StreamView } from './StreamView';
import { PermissionCard } from './PermissionCard';
import { QuestionCard } from './QuestionCard';
import { PromptInput } from './PromptInput';
import { ModeSelector } from './ModeSelector';
import { InterruptButton } from './InterruptButton';
import { UsageBar } from './UsageBar';

export function SessionScreen({ state, client }: { state: ClientState; client: HarnessClient }) {
  const live = state.phase === 'live';
  const { session } = state;

  return (
    <div className="app">
      <div className="header">
        <strong>Harness</strong>
        <ConnectionBadge state={state} />
        <span className="hint">{session.status}</span>
        <UsageBar usage={session.usage} />
        <span className="spacer" />
        <ModeSelector client={client} mode={session.mode} disabled={!live} />
        <InterruptButton client={client} disabled={!live || !session.turnActive} />
      </div>

      <StreamView session={session} myDeviceId={state.myDeviceId} />

      {session.pendingPermissions.map((p) => (
        <PermissionCard key={p.requestId} view={p} client={client} />
      ))}
      {session.pendingQuestions.map((q) => (
        <QuestionCard key={q.questionId} view={q} client={client} />
      ))}

      <PromptInput client={client} disabled={false} />
    </div>
  );
}
