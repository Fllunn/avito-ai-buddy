import { Icon, type IconName } from './components/Icon';
import { Logo } from './components/Logo';

const prompts: { label: string; icon: IconName; color: string }[] = [
  { label: 'С чего начать?', icon: 'spark', color: '#B8DAFB' },
  { label: 'Нужен доступ', icon: 'lock', color: '#DFCBF4' },
  { label: 'Нужен отгул', icon: 'calendar', color: '#C8FFC0' },
];

export function App() {
  return (
    <div className="trainee-app">
      <header className="topbar">
        <Logo />
        <span className="demo-label">Демонстрационный прототип</span>
      </header>
      <main className="empty-chat">
        <div className="decor decor-a" aria-hidden="true" />
        <div className="decor decor-b" aria-hidden="true" />
        <div className="decor decor-c" aria-hidden="true" />
        <div className="welcome">
          <div className="buddy-mark"><Icon name="spark" size={25} /></div>
          <h1>Привет! С чего начнём?</h1>
          <p>Я AI-buddy — помогу освоиться, найти нужный сервис или оформить запрос.</p>
          <div className="composer large">
            <input disabled placeholder="Напишите AI-buddy..." aria-label="Сообщение AI-buddy" aria-describedby="bootstrap-status" />
            <span className="composer-hint">AI может ошибаться — важные решения лучше проверить</span>
            <button disabled aria-label="Отправить"><Icon name="send" size={18} /></button>
          </div>
          <div className="prompt-list" aria-describedby="bootstrap-status">
            {prompts.map((prompt) => (
              <button key={prompt.label} disabled>
                <span style={{ background: prompt.color }}><Icon name={prompt.icon} size={17} /></span>
                {prompt.label}
                <Icon name="chevron" size={15} />
              </button>
            ))}
          </div>
          <p id="bootstrap-status" className="bootstrap-note" role="status">Прототип в разработке. Чат и план онбординга пока недоступны.</p>
        </div>
      </main>
    </div>
  );
}
