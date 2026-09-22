import { useState } from 'react';
import { Pressable, StyleSheet, Text, TextInput, View } from 'react-native';
import type { HarnessClient } from '@harness/protocol';

export function PromptInput({ client }: { client: HarnessClient }) {
  const [text, setText] = useState('');

  const send = () => {
    const value = text.trim();
    if (!value) return;
    setText('');
    void client.sendPrompt(value).catch(() => {
      /* offline queue and errors surface via connection state */
    });
  };

  return (
    <View style={styles.row}>
      <TextInput
        style={styles.input}
        value={text}
        onChangeText={setText}
        placeholder="Send a prompt…"
        placeholderTextColor="#64748b"
        multiline
      />
      <Pressable style={styles.button} onPress={send} disabled={text.trim() === ''}>
        <Text style={styles.buttonText}>Send</Text>
      </Pressable>
    </View>
  );
}

const styles = StyleSheet.create({
  row: { flexDirection: 'row', gap: 8, alignItems: 'flex-end' },
  input: {
    flex: 1,
    maxHeight: 120,
    borderWidth: 1,
    borderColor: '#26344f',
    borderRadius: 8,
    color: '#e2e8f0',
    paddingHorizontal: 10,
    paddingVertical: 8,
    backgroundColor: '#1b2740',
  },
  button: { backgroundColor: '#0369a1', borderRadius: 8, paddingHorizontal: 16, paddingVertical: 10 },
  buttonText: { color: '#f8fafc', fontWeight: '600' },
});
