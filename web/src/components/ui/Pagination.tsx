import { ChevronLeftRegular, ChevronRightRegular } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { useI18n } from "../../lib/i18n";
import { Button } from "./Button";
import { Select } from "./Select";

export interface PaginationProps {
  /** Current page, 1-based. */
  page: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  onPageSizeChange?: (pageSize: number) => void;
  pageSizeOptions?: number[];
  className?: string;
}

type PageItem = number | "ellipsis";

// Build the page-number strip: always show the first and last page, the pages
// around the current one, and collapse longer gaps into a single ellipsis
// (filling a gap of exactly one page with that page's number).
function pageWindow(current: number, pages: number): PageItem[] {
  if (pages <= 7) {
    return Array.from({ length: pages }, (_, index) => index + 1);
  }
  const left = Math.max(2, current - 1);
  const right = Math.min(pages - 1, current + 1);
  const kept: number[] = [];
  for (let i = 1; i <= pages; i++) {
    if (i === 1 || i === pages || (i >= left && i <= right)) {
      kept.push(i);
    }
  }
  const items: PageItem[] = [];
  let previous = 0;
  for (const page of kept) {
    if (previous !== 0) {
      if (page - previous === 2) items.push(previous + 1);
      else if (page - previous > 2) items.push("ellipsis");
    }
    items.push(page);
    previous = page;
  }
  return items;
}

export function Pagination({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = [10, 20, 50],
  className,
}: PaginationProps) {
  const { t } = useI18n();
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const current = Math.min(Math.max(1, page), pages);
  const items = pageWindow(current, pages);

  if (total <= 0) return null;

  return (
    <div className={cx("flex flex-wrap items-center gap-x-3 gap-y-2", className)}>
      <span className="text-xs text-gray-400">{t("共 {total} 条").replace("{total}", String(total))}</span>
      <div className="flex items-center gap-1 sm:ml-auto">
        {onPageSizeChange ? (
          <Select
            value={String(pageSize)}
            onChange={(value) => onPageSizeChange(Number(value))}
            options={pageSizeOptions.map((count) => ({
              value: String(count),
              label: t("{count} 条/页").replace("{count}", String(count)),
            }))}
            className="mr-1 w-24"
          />
        ) : null}
        <Button
          size="small"
          icon={<ChevronLeftRegular />}
          disabled={current <= 1}
          onClick={() => onPageChange(current - 1)}
          aria-label={t("上一页")}
        />
        {items.map((item, index) =>
          item === "ellipsis" ? (
            <span key={`ellipsis-${index}`} className="px-1 text-xs text-gray-400">
              …
            </span>
          ) : (
            <Button
              key={item}
              size="small"
              variant={item === current ? "primary" : "text"}
              onClick={() => onPageChange(item)}
              aria-current={item === current ? "page" : undefined}
            >
              {item}
            </Button>
          ),
        )}
        <Button
          size="small"
          icon={<ChevronRightRegular />}
          disabled={current >= pages}
          onClick={() => onPageChange(current + 1)}
          aria-label={t("下一页")}
        />
      </div>
    </div>
  );
}
