import { useState, type FormEvent } from 'react';
import type { HarnessClient, QuestionView } from '@harness/protocol';
import { PlainText } from './PlainText';

export function QuestionCard({ view, client }: { view: QuestionView; client: HarnessClient }) {
  const [answer, setAnswer] = useState('');

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const text = answer;
    setAnswer('');
    void client.answerQuestion({ questionId: view.questionId, text }).catch(() => {
      /* resolution errors surface as stream notices */
    });
  };

  return (
    <form className="card question" onSubmit={submit}>
      <div className="meta">question</div>
      <PlainText text={view.text} />
      <div className="composer">
        <input
          value={answer}
          onChange={(e) => setAnswer(e.target.value)}
          placeholder="Your answer"
          aria-label="Answer"
        />
        <button type="submit" disabled={answer.trim() === ''}>
          Answer
        </button>
      </div>
    </form>
  );
}
