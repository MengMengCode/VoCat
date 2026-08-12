import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { api } from "../api";
import { EN_DICT } from "./i18n-en";

export type Language = "en" | "zh";

interface I18nValue {
  lang: Language;
  setLanguage: (lang: Language) => void;
  /** 以中文为键查英文；中文模式或无词条时原文返回。 */
  t: (text: string) => string;
}

const I18nContext = createContext<I18nValue | null>(null);

// 模块级当前语言镜像：供非组件代码（工具函数、事件回调）翻译用。
let activeLang: Language = "en";

export function tl(text: string): string {
  if (!text || activeLang === "zh") return text;
  return EN_DICT[text] ?? text;
}

/** 带插值的翻译：tf("确定删除实例 {id}？", { id }) —— 词典键为含 {占位} 的模板。 */
export function tf(pattern: string, vars: Record<string, unknown>): string {
  let text = tl(pattern);
  for (const [key, value] of Object.entries(vars)) {
    text = text.replaceAll(`{${key}}`, String(value));
  }
  return text;
}

export function LanguageProvider({ children }: { children: ReactNode }) {
  const [lang, setLang] = useState<Language>("en");

  useEffect(() => {
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
    document.title = lang === "zh" ? "vocat · 高通模块专业测试工具" : "vocat · Qualcomm Module Professional Test Tool";
  }, [lang]);

  // 语言偏好存数据库（GET 无需鉴权）：任意设备/浏览器打开都是同一种语言。
  useEffect(() => {
    let cancelled = false;
    api<{ language?: string }>("/settings/preferences")
      .then((data) => {
        if (!cancelled && (data.language === "en" || data.language === "zh")) {
          activeLang = data.language;
          setLang(data.language);
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const setLanguage = useCallback((next: Language) => {
    activeLang = next;
    setLang(next);
    // 未登录时（登录页）PUT 会 401，静默忽略；登录后的选择会写入数据库。
    api("/settings/preferences", { method: "PUT", body: { language: next } }).catch(() => {});
  }, []);

  // 委托给模块级 tl（读取实时 activeLang），使 t 的函数身份稳定：
  // 否则 useCallback 闭包会捕获到旧语言的 t，切换语言后出现标题/正文语言不一致。
  const t = useCallback((text: string) => tl(text), []);

  const value = useMemo(() => ({ lang, setLanguage, t }), [lang, setLanguage, t]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error("useI18n must be used within LanguageProvider");
  return ctx;
}
