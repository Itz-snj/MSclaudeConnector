import { useState } from 'react';
import { Pressable, StyleSheet, Text, TextInput, View } from 'react-native';
import type { HarnessClient, QuestionView } from '@harness/protocol';
import { PlainText } from './PlainText';

export function QuestionCard({ view, client }: { view: QuestionView; client: HarnessClient }) {
  const [answer, setAnswer] = useState('');

  const submit = () => {
    const text = answer;
    if (!text.trim()) return;
    setAnswer('');
    void client.answerQuestion({ questionId: view.questionId, text }).catch(() => {
      /* errors surface as stream notices */
    });
  };

  return (
    <View style={styles.card}>
      <Text style={styles.meta}>question</Text>
      <PlainText text={view.text} />
      <View style={styles.row}>
        <TextInput
          style={styles.input}
          value={answer}
          onChangeText={setAnswer}
          placeholder="Your answer"
          placeholderTextColor="#64748b"
        />
        <Pressable style={styles.button} onPress={submit}>
          <Text style={styles.buttonText}>Answer</Text>
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
  row: { flexDirection: 'row', gap: 8 },
  input: {
    flex: 1,
    borderWidth: 1,
    borderColor: '#26344f',
    borderRadius: 6,
    color: '#e2e8f0',
    paddingHorizontal: 10,
    paddingVertical: 8,
  },
  button: { backgroundColor: '#0369a1', borderRadius: 6, paddingHorizontal: 14, justifyContent: 'center' },
  buttonText: { color: '#f8fafc', fontWeight: '600' },
});
