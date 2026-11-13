import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { App } from './App';

describe('Application bootstrap', () => {
  it('shows the supplied welcome design and marks the prototype', () => {
    render(<App />);
    expect(screen.getByRole('heading', { name: 'Привет! С чего начнём?', level: 1 })).toBeInTheDocument();
    expect(screen.getByText('Avito Start')).toBeInTheDocument();
    expect(screen.getByText('Демонстрационный прототип')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Чат и план онбординга пока недоступны');
  });

  it('does not simulate actions or collect messages before the backend exists', () => {
    render(<App />);
    expect(screen.getByRole('textbox', { name: 'Сообщение AI-buddy' })).toBeDisabled();
    for (const name of ['Отправить', 'С чего начать?', 'Нужен доступ', 'Нужен отгул']) {
      expect(screen.getByRole('button', { name })).toBeDisabled();
    }
  });
});
