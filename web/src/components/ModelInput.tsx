import { Check, ChevronDown } from 'lucide-react';
import { useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';

export default function ModelInput({ value, models, onChange }: {
  value: string;
  models: string[];
  onChange: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const toggle = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const hasModels = models.length > 0;
  const expanded = hasModels && open;

  useLayoutEffect(() => {
    if (expanded) {
      const current = list.current?.querySelector<HTMLElement>('[aria-selected="true"]');
      (current ?? list.current?.querySelector<HTMLElement>('[role="option"]'))?.focus();
    }
  }, [expanded]);

  function close() {
    toggle.current?.focus();
    setOpen(false);
  }

  function pick(model: string) {
    onChange(model);
    close();
  }

  function listKeys(e: KeyboardEvent<HTMLUListElement>) {
    const options = Array.from(e.currentTarget.querySelectorAll<HTMLElement>('[role="option"]'));
    const active = options.findIndex((el) => el === e.target);
    switch (e.key) {
      case 'ArrowDown':
        options[Math.min(active + 1, options.length - 1)]?.focus();
        break;
      case 'ArrowUp':
        options[Math.max(active - 1, 0)]?.focus();
        break;
      case 'Home':
        options[0]?.focus();
        break;
      case 'End':
        options.at(-1)?.focus();
        break;
      case 'Enter':
      case ' ':
        if (active >= 0) pick(models[active]);
        break;
      case 'Escape':
        close();
        e.stopPropagation();
        break;
      default:
        return;
    }
    e.preventDefault();
  }

  return (
    <div className="settings-model-picker" onBlur={(e) => {
      if (!e.currentTarget.contains(e.relatedTarget)) setOpen(false);
    }}>
      <input id="settings-api-model" value={value} required autoComplete="off" spellCheck={false}
        placeholder={hasModels ? '选择模型或输入 ID' : '输入模型 ID'}
        onChange={(e) => { setOpen(false); onChange(e.target.value); }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && hasModels) {
            e.preventDefault();
            if (expanded) list.current?.querySelector<HTMLElement>('[role="option"]')?.focus();
            else setOpen(true);
          }
        }} />
      {hasModels && <button ref={toggle} type="button" className="settings-model-toggle"
        aria-label="可用模型列表" aria-haspopup="listbox" aria-controls={expanded ? 'settings-model-list' : undefined}
        aria-expanded={expanded} onClick={() => setOpen(!expanded)}>
        <ChevronDown size={17} aria-hidden="true" />
      </button>}
      {expanded && <ul ref={list} id="settings-model-list" role="listbox" aria-label="可用模型"
        className="settings-model-list" onKeyDown={listKeys}>
        {models.map((model) => (
          <li key={model} role="option" tabIndex={-1} aria-selected={model === value} onClick={() => pick(model)}>
            <span>{model}</span>
            {model === value && <Check size={14} aria-hidden="true" />}
          </li>
        ))}
      </ul>}
    </div>
  );
}
