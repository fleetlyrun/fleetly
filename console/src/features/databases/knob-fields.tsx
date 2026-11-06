import { useId, useState } from "react";
import { StatusBadge } from "@/components/domain/status-badge";
import { Select } from "@/components/kit/ui/native-select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  composeSizeValue,
  groupKnobsByCategory,
  knobClassBadge,
  knobControlFor,
  knobFormatHint,
  parseSizeValue,
  setKnobValue,
  SIZE_UNITS,
  type EngineKnob,
  type KnobBadge,
  type KnobValues,
  type SizeUnit,
} from "@/kit/data-engines";

// 旋钮表单块（B3，design §3.5）：Engine Catalog 驱动——按 Category 分组、
// Kind→控件映射（bool = 开关 / int = 数字输入 / size = 数字输入 + 单位 /
// enum = 下拉 / duration = 文本 + 格式提示）、class 徽标（instant / rolling）。
// 创建对话框与 Settings tab 共用（零硬编码引擎页面）。受控块：values 是
// 旋钮期望态（键在场 = 已设置；缺席 = 引擎缺省），onChange 产出新期望态；
// 值域校验不复刻——服务端受理位（ValidateSettings）单源，拒绝消息走结果盒。

const BADGE_TITLE: Record<KnobBadge, string> = {
  instant: "Instant: takes effect live, no restart",
  rolling: "Rolling: takes effect through a supervised rolling restart of the database carrier",
};

// KnobClassBadge：class 徽标（instant = dynamic 运行时生效；rolling = static
// 受监督滚动）。语义色走 StatusBadge 单源（不私造配色）。
export function KnobClassBadge({ knobClass }: { knobClass: string | undefined }) {
  const badge = knobClassBadge(knobClass);
  return (
    <span title={BADGE_TITLE[badge]}>
      <StatusBadge tone={badge === "instant" ? "success" : "warning"}>{badge}</StatusBadge>
    </span>
  );
}

export function KnobFields({
  knobs,
  values,
  onChange,
  disabled = false,
}: {
  knobs: ReadonlyArray<EngineKnob> | undefined;
  values: KnobValues;
  onChange: (next: KnobValues) => void;
  disabled?: boolean;
}) {
  const groups = groupKnobsByCategory(knobs);
  if (groups.length === 0) {
    return <p className="text-2xs text-muted">This engine exposes no knobs yet — it runs on engine defaults.</p>;
  }
  return (
    <div className="flex flex-col gap-3">
      {groups.map((group) => (
        <fieldset key={group.category} className="flex flex-col gap-2">
          <legend className="mb-1 text-2xs font-semibold tracking-wide text-muted uppercase">{group.category}</legend>
          {group.knobs.map((knob) => {
            const key = knob.key ?? "";
            return (
              <KnobRow
                key={key}
                knob={knob}
                value={Object.prototype.hasOwnProperty.call(values, key) ? values[key] : undefined}
                disabled={disabled}
                onChange={(value) => onChange(setKnobValue(values, key, value))}
              />
            );
          })}
        </fieldset>
      ))}
    </div>
  );
}

function KnobRow({
  knob,
  value,
  onChange,
  disabled,
}: {
  knob: EngineKnob;
  value: string | undefined;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const id = useId();
  const key = knob.key ?? "";
  const set = value !== undefined;
  const hint = knobFormatHint(knob);
  return (
    <div className="rounded-lg border bg-panel-2/30 px-3 py-2" data-knob={key}>
      <div className="flex items-center justify-between gap-2">
        <label htmlFor={id} className="font-mono text-xs font-medium">
          {key}
        </label>
        <div className="flex items-center gap-1.5">
          <KnobClassBadge knobClass={knob.class} />
          {set ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 px-1.5 text-2xs"
              disabled={disabled}
              aria-label={`Use the engine default for ${key}`}
              onClick={() => onChange("")}
            >
              Use default
            </Button>
          ) : null}
        </div>
      </div>
      <div className="mt-1.5">
        <KnobControlInput id={id} knob={knob} value={value} onChange={onChange} disabled={disabled} />
      </div>
      <p className="mt-1 text-2xs text-muted">
        {set ? "Set explicitly" : knob.default_value ? `Engine default: ${knob.default_value}` : "Engine default (unset)"}
        {hint ? ` · ${hint}` : ""}
      </p>
    </div>
  );
}

function KnobControlInput({
  id,
  knob,
  value,
  onChange,
  disabled,
}: {
  id: string;
  knob: EngineKnob;
  value: string | undefined;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const control = knobControlFor(knob.kind);
  switch (control) {
    case "switch": {
      // 开关显示生效值（已设置值 ?? 引擎缺省）；拨动即显式设置——「Use
      // default」回到缺省（键移除）。
      const effective = value ?? knob.default_value ?? "false";
      return (
        <div className="flex items-center gap-2">
          <Switch
            id={id}
            checked={effective === "true"}
            disabled={disabled}
            onCheckedChange={(checked) => onChange(checked ? "true" : "false")}
          />
          <span className="font-mono text-xs text-muted">{effective}</span>
        </div>
      );
    }
    case "number":
      return (
        <Input
          id={id}
          type="number"
          inputMode="numeric"
          step={1}
          min={knob.min || undefined}
          max={knob.max || undefined}
          className="h-8 font-mono text-xs"
          placeholder={knob.default_value || "unset"}
          value={value ?? ""}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value.trim())}
        />
      );
    case "size":
      return <SizeInput id={id} knob={knob} value={value} onChange={onChange} disabled={disabled} />;
    case "select":
      return (
        <Select
          id={id}
          className="font-mono text-xs"
          value={value ?? ""}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
        >
          <option value="">{knob.default_value ? `engine default (${knob.default_value})` : "engine default"}</option>
          {(knob.enum_values ?? []).map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </Select>
      );
    default:
      return (
        <Input
          id={id}
          className="h-8 font-mono text-xs"
          placeholder={knob.default_value || (control === "duration" ? "e.g. 30s" : "unset")}
          value={value ?? ""}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value.trim())}
        />
      );
  }
}

// SizeInput：数值 + 单位（受理口径 <整数><b|kb|mb|gb>）。未设置时单位选择
// 暂存本地（先选单位再填数值不回弹）；数值清空 = 回到引擎缺省。
function SizeInput({
  id,
  knob,
  value,
  onChange,
  disabled,
}: {
  id: string;
  knob: EngineKnob;
  value: string | undefined;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const fallback = parseSizeValue(knob.default_value);
  const [pendingUnit, setPendingUnit] = useState<SizeUnit>(fallback.unit);
  const parsed = value !== undefined ? parseSizeValue(value) : { amount: "", unit: pendingUnit };
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        min={0}
        step={1}
        className="h-8 font-mono text-xs"
        placeholder={fallback.amount || "unset"}
        value={parsed.amount}
        disabled={disabled}
        onChange={(event) => onChange(composeSizeValue(event.target.value, parsed.unit))}
      />
      <Select
        aria-label={`${knob.key ?? "size"} unit`}
        className="w-20 font-mono text-xs"
        value={parsed.unit}
        disabled={disabled}
        onChange={(event) => {
          const unit = event.target.value as SizeUnit;
          setPendingUnit(unit);
          if (parsed.amount !== "") onChange(composeSizeValue(parsed.amount, unit));
        }}
      >
        {SIZE_UNITS.map((unit) => (
          <option key={unit} value={unit}>
            {unit}
          </option>
        ))}
      </Select>
    </div>
  );
}

// ResourcesFields：载体资源上限（design §5：可选；空 = 无限制 = 现状）。OOM
// 诚实立场随文案披露（硬上限是用户裁定，引擎共享内存/连接内存可能顶破）。
export function ResourcesFields({
  cpuMillis,
  memoryMb,
  onChange,
  disabled = false,
}: {
  cpuMillis: string;
  memoryMb: string;
  onChange: (next: { cpuMillis: string; memoryMb: string }) => void;
  disabled?: boolean;
}) {
  const cpuId = useId();
  const memoryId = useId();
  return (
    <div className="flex flex-col gap-1.5">
      <div className="grid grid-cols-2 gap-2">
        <div className="flex flex-col gap-1">
          <label htmlFor={cpuId} className="text-xs font-medium text-muted">
            CPU limit (millicores)
          </label>
          <Input
            id={cpuId}
            type="number"
            inputMode="numeric"
            min={0}
            step={100}
            className="h-8 font-mono text-xs"
            placeholder="unlimited"
            value={cpuMillis}
            disabled={disabled}
            onChange={(event) => onChange({ cpuMillis: event.target.value.trim(), memoryMb })}
          />
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor={memoryId} className="text-xs font-medium text-muted">
            Memory limit (MiB)
          </label>
          <Input
            id={memoryId}
            type="number"
            inputMode="numeric"
            min={0}
            step={64}
            className="h-8 font-mono text-xs"
            placeholder="unlimited"
            value={memoryMb}
            disabled={disabled}
            onChange={(event) => onChange({ cpuMillis, memoryMb: event.target.value.trim() })}
          />
        </div>
      </div>
      <p className="text-2xs text-muted">
        Empty = unlimited (the default). A hard memory limit is your call: shared buffers and per-connection memory can push
        the engine past it and get it restarted. Changing limits rolls the carrier.
      </p>
    </div>
  );
}
