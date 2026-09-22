import { Pressable, StyleSheet, Text, View } from 'react-native';
import type { HarnessClient, PermissionView } from '@harness/protocol';
import { PlainText } from './PlainText';

export function PermissionCard({ view, client }: { view: PermissionView; client: HarnessClient }) {
  const answer = (allow: boolean, allowAlways = false) => {
    void client.answerPermission({ requestId: view.requestId, allow, allowAlways }).catch(() => {
      /* errors surface as stream notices */
    });
  };
  return (
    <View style={styles.card}>
      <Text style={styles.meta}>permission · {view.kind}</Text>
      <PlainText text={view.summary} />
      <View style={styles.actions}>
        <Pressable style={styles.allow} onPress={() => answer(true)}>
          <Text style={styles.buttonText}>Allow</Text>
        </Pressable>
        <Pressable style={styles.allow} onPress={() => answer(true, true)}>
          <Text style={styles.buttonText}>Always</Text>
        </Pressable>
        <Pressable style={styles.deny} onPress={() => answer(false)}>
          <Text style={styles.buttonText}>Deny</Text>
        </Pressable>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  card: {
    borderWidth: 1,
    borderColor: '#fbbf24',
    borderRadius: 10,
    padding: 10,
    gap: 8,
    backgroundColor: 'rgba(251,191,36,0.08)',
  },
  meta: { color: '#94a3b8', fontSize: 11, textTransform: 'uppercase' },
  actions: { flexDirection: 'row', gap: 8 },
  allow: { backgroundColor: '#166534', borderRadius: 6, paddingHorizontal: 12, paddingVertical: 8 },
  deny: { backgroundColor: '#7f1d1d', borderRadius: 6, paddingHorizontal: 12, paddingVertical: 8 },
  buttonText: { color: '#f8fafc', fontWeight: '600' },
});
