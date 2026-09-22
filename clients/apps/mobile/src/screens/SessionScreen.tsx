import { StyleSheet, Text, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import type { ClientState, HarnessClient } from '@harness/protocol';
import { ConnectionBadge } from '../components/ConnectionBadge';
import { StreamView } from '../components/StreamView';
import { PermissionCard } from '../components/PermissionCard';
import { QuestionCard } from '../components/QuestionCard';
import { PromptInput } from '../components/PromptInput';

export function SessionScreen({ state, client }: { state: ClientState; client: HarnessClient }) {
  const insets = useSafeAreaInsets();
  const { session } = state;

  return (
    <View style={[styles.container, { paddingTop: insets.top + 8, paddingBottom: insets.bottom + 8 }]}>
      <View style={styles.header}>
        <Text style={styles.title}>Harness</Text>
        <ConnectionBadge state={state} />
        <Text style={styles.hint}>{session.status}</Text>
      </View>

      <StreamView session={session} myDeviceId={state.myDeviceId} />

      {session.pendingPermissions.map((p) => (
        <PermissionCard key={p.requestId} view={p} client={client} />
      ))}
      {session.pendingQuestions.map((q) => (
        <QuestionCard key={q.questionId} view={q} client={client} />
      ))}

      <PromptInput client={client} />
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#0b1220', paddingHorizontal: 12, gap: 8 },
  header: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  title: { color: '#38bdf8', fontSize: 18, fontWeight: '700' },
  hint: { color: '#94a3b8', fontSize: 12 },
});
