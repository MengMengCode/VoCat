import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { ArrowRightRegular, LockClosedRegular, PersonRegular } from "@fluentui/react-icons";
import { useAuth } from "../store/auth";
import { useI18n } from "../lib/i18n";
import { message } from "../components/ui/message";
import { BrandLogo } from "../components/shell/BrandLogo";
import { getSSOProviders } from "../api";
import type { SSOProviders } from "../types";
import { safeRedirect } from "../lib/safeRedirect";
import { ssoErrorMessage, SSO_RESULT_MESSAGES } from "../lib/sso";

const INPUT_CLASS =
  "w-full rounded-lg border border-gray-200 bg-white/70 py-3 pl-10 pr-4 font-mono text-sm text-gray-900 placeholder-gray-400 outline-none transition-all focus:border-indigo-500/40 focus:ring-2 focus:ring-indigo-500/25 dark:border-white/10 dark:bg-black/20 dark:text-gray-100 dark:placeholder-gray-500";

/** Offer local and enabled SSO sign-in while preserving safe return paths and callback notices. */
export default function LoginPage() {
  const { login } = useAuth();
  const { t } = useI18n();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [working, setWorking] = useState(false);
  const [providers, setProviders] = useState<SSOProviders | null>(null);
  const [ssoNotice, setSSONotice] = useState<{ error: boolean; text: string } | null>(null);
  const redirect = safeRedirect(searchParams.get("redirect"), window.location.origin);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    /** Retry transient metadata failures at most three times without delaying local sign-in. */
    const load = (attempt: number) => {
      void getSSOProviders().then((data) => {
        if (!cancelled) setProviders(data);
      }).catch(() => {
        if (!cancelled && attempt < 3) {
          timer = window.setTimeout(() => load(attempt + 1), 1000 * 2 ** attempt);
        }
      });
    };
    load(0);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, []);

  useEffect(() => {
    const error = searchParams.get("sso_error");
    const result = searchParams.get("sso_result");
    if (!error && !result) return;
    if (error) setSSONotice({ error: true, text: ssoErrorMessage(error) });
    else if (result && Object.hasOwn(SSO_RESULT_MESSAGES, result)) setSSONotice({ error: false, text: SSO_RESULT_MESSAGES[result] });
    const next = new URLSearchParams(searchParams);
    next.delete("sso_error");
    next.delete("sso_result");
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);

  /** Validate local credentials and navigate to the safe return path after a successful password login. */
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!username || !password) {
      message.warning(t("请输入用户名和密码"));
      return;
    }
    setWorking(true);
    await new Promise((resolve) => setTimeout(resolve, 600));
    const ok = await login(username, password);
    setWorking(false);
    if (ok) {
      message.success(t("欢迎回来"));
      navigate(redirect, { replace: true });
    } else {
      message.error(t("登录失败，请检查凭证"));
    }
  }

  return (
    <div className="relative flex h-full w-full items-center justify-center overflow-hidden">
      <div className="animate-pulse-slow absolute -left-32 -top-32 h-[520px] w-[520px] rounded-full bg-indigo-500/15 blur-[120px] dark:bg-indigo-500/20" />
      <div className="animate-pulse-slow absolute -bottom-32 -right-32 h-[520px] w-[520px] rounded-full bg-indigo-500/12 blur-[120px] [animation-delay:2s] dark:bg-indigo-500/16" />
      <div className="relative w-full max-w-md p-1">
        <div className="group relative overflow-hidden rounded-2xl border border-gray-100 bg-white/70 p-8 shadow-2xl backdrop-blur-xl dark:border-white/10 dark:bg-[#141418]/70">
          <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-indigo-500/8 to-transparent opacity-0 transition-opacity duration-500 group-hover:opacity-100" />
          <div className="relative z-10 mb-10 text-center">
            <div className="mx-auto mb-6 flex h-20 w-20 items-center justify-center rounded-2xl bg-white shadow-lg shadow-indigo-500/20 ring-1 ring-black/5 transition-transform duration-300 group-hover:scale-105 dark:bg-white/10 dark:ring-white/10">
              <BrandLogo className="h-14 w-14" />
            </div>
            <h2 className="bg-gradient-to-r from-gray-900 to-gray-600 bg-clip-text text-3xl font-bold text-transparent dark:from-white dark:to-gray-400">
              vocat
            </h2>
            <p className="mt-3 text-sm tracking-wide text-gray-500 dark:text-gray-400">{t("高通模块专业测试工具")}</p>
          </div>
          {ssoNotice ? (
            <div role={ssoNotice.error ? "alert" : "status"} className={`relative z-10 mb-5 rounded-lg border px-3 py-3 text-sm leading-relaxed ${ssoNotice.error ? "border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-200" : "border-sky-200 bg-sky-50 text-sky-800 dark:border-sky-500/25 dark:bg-sky-500/10 dark:text-sky-200"}`}>
              {t(ssoNotice.text)}
            </div>
          ) : null}
          <form onSubmit={submit} className="relative z-10 space-y-6">
            <div className="space-y-2">
              <div className="relative">
                <div className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3 text-gray-400 dark:text-gray-500">
                  <PersonRegular className="h-5 w-5" />
                </div>
                <input
                  className={INPUT_CLASS}
                  placeholder={t("用户名")}
                  type="text"
                  autoComplete="username"
                  value={username}
                  onChange={(event) => setUsername(event.target.value)}
                />
              </div>
            </div>
            <div className="space-y-2">
              <div className="relative">
                <div className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3 text-gray-400 dark:text-gray-500">
                  <LockClosedRegular className="h-5 w-5" />
                </div>
                <input
                  className={INPUT_CLASS}
                  placeholder={t("密码")}
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                />
              </div>
            </div>
            <button
              type="submit"
              disabled={working}
              className="flex w-full items-center justify-center gap-2 rounded-lg bg-[#0ea5e9] px-4 py-3 font-bold text-white shadow-sm transition-all duration-200 hover:bg-[#0284c7] active:scale-95 disabled:cursor-not-allowed disabled:opacity-70"
            >
              {working ? (
                <span className="h-5 w-5 animate-spin rounded-full border-2 border-white/30 border-t-white" />
              ) : (
                <span>{t("登录")}</span>
              )}
              {!working && <ArrowRightRegular className="h-5 w-5" />}
            </button>
          </form>
          {providers?.sso.enabled ? (
            <div className="relative z-10 mt-6 border-t border-gray-200 pt-5 dark:border-white/10">
              <a
                href={`/api/auth/sso/start?${new URLSearchParams({ redirect }).toString()}`}
                className="flex w-full items-center justify-center rounded-lg border border-sky-300 bg-sky-50 px-4 py-3 text-sm font-semibold text-sky-700 transition-colors hover:bg-sky-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-500 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-300 dark:hover:bg-sky-500/20"
              >
                {t("通过 {provider} 登录").replace("{provider}", providers.sso.displayName || "SSO")}
              </a>
            </div>
          ) : null}
        </div>
        <div className="mt-6 text-center">
          <p className="text-xs text-gray-500">vocat © 2026</p>
        </div>
      </div>
    </div>
  );
}
