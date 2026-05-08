# depgraph ドメイン

depgraph 内部で使われる用語とエンティティの定義。新規にコードを書く前、
あるいは既存コードを読む前に参照する想定。

## 主要な関心ごと

depgraph の本質は次の 3 点に集約される。`one-shot 解析` (HTTP モード) と
`live セッション` (MCP モード) は同じ関心ごとを別の時間軸で扱っているに
すぎない。

1. **ソースツリーのシンボル依存グラフを構築・維持する**
   どのシンボルが、どのシンボルから参照されているか。LSP-based static
   analysis は現実装手段で、実装の詳細にすぎない。
2. **そのグラフをクエリ可能にする**
   名前で symbol を引く (`find_symbols`) / symbol から呼び出し元チェーンを
   辿る (`find_references`)。
3. **ソースの変化に追随する**
   ある時点のソースを真実とするので、ソース変更時にどう同期するかが
   全モードでの共通課題となる。

## 必然的に発生する手続き

モード (HTTP / MCP / 将来の IDE プラグイン) を問わず通る手順。

1. **検出 (Detect)** — 与えられたディレクトリにどの言語があるか判定する。
2. **取得 (Acquire)** — 各言語の解析バックエンドを確保する (現状は LSP)。
3. **索引化 (Index)** — 初期解析を走らせ、シンボルと参照の集合を得る。
4. **照会 (Query)** — シンボル検索・参照辿りに応える。
5. **同期 (Sync)** — 元ソースが変わったら index を最新化する。
6. **解放 (Tear down)** — バックエンドプロセスを reap、watcher を止める。

## コア語彙

### エンティティ・値オブジェクト

| 名称 | 種別 | 意味 |
|---|---|---|
| **Project** | エンティティ | depgraph に登録された 1 つのプロジェクトルート。`IndexState` (Indexing/Ready/Failed) を持つライフサイクル管理単位。MCP の `add_project` ツールで登録される単位。 |
| **Workspace** | アグリゲート | 1 つの depgraph セッション内で登録された全 Project を束ねる。`AddProject`, `Get`, `Shutdown` を持つ。 |
| **Language** | 値 | Go / Rust / TypeScript の列挙 (`lsploader.Language`)。 |
| **Symbol** | 値オブジェクト | `find_symbols` / `find_references` が返す「識別可能なコード要素」。`{id, name, kind, path, line, character}`。 |
| **SymbolID** | 値オブジェクト | ステートレスにシンボルを表す wire 表現。`<lang>:<base64(rel_path)>:<line>:<char>:<base64(name)>`。サーバ側状態に依存しない。 |
| **DocumentSymbol** | 値オブジェクト | LSP の `textDocument/documentSymbol` の domain 表現。階層構造 (Children) を持つ。 |
| **CallerChain** | 結果型 | あるシンボルから上流に向けて辿った Symbol の集合。`PartialResult[Symbol]` として返る。 |
| **IndexState** | 値 | Indexing → Ready / Failed の遷移を表す。Project ごとに保持。 |
| **SourceChange** | 値オブジェクト | `{Path, Op}`。`Op` は Created / Modified / Deleted。 |
| **PartialResult\<T\>** | ラッパ | `{Results, Warnings}`。一部の LSP 呼び出しが失敗しても部分結果を呼び出し側に返せる。 |

### ポート (interface)

domain 側が依存する I/O の境界。adapter で具体実装される。

| 名称 | 役割 |
|---|---|
| **AnalysisSession** | 1 言語に対する live な解析バックエンドの抽象。`DocumentSymbol`, `References`, 状態同期 (`DidOpen`, `DidChange`, `DidClose`) を提供。LSP に縛られない名前。 |
| **AnalysisSessionFactory** | `(Language, Root, Excludes, FileEvents) → AnalysisSession` を起動する factory。 |
| **SourceWatcher** | あるルート以下のファイル変更をイベントストリームとして配信。 |
| **SourceWatcherFactory** | `(Root, Excludes) → SourceWatcher` を起動する factory。 |
| **LanguageDetector** | ルートから検出された Language のリストを返す。`lsploader.Detect` のラッパが現実装。 |

## 「domain か adapter か」の判定基準

迷ったときの基準:

- **依存方向**: 標準ライブラリ + `domain/` 内の他型のみに依存できれば
  domain 候補。`os/exec`, `net/url`, `fsnotify`, `encoding/json` 等の I/O
  ライブラリに依存するなら adapter。
- **モード非依存**: HTTP one-shot / MCP / 将来 IDE のどれでも同じ概念として
  登場するなら domain。MCP 固有 (例: `tools.json`、stdio JSON-RPC 用語)
  なら adapter (`mcp/`)。
- **ライフサイクル**: 純粋なデータ・関数なら値オブジェクト or 純粋関数。
  状態を持つ・I/O を呼ぶならエンティティ + ポート分離。

## 用語の衝突に関する注意

- C4 アーキテクチャモデルでは "Component" がモジュール分割単位を指す
  (`_docs/architecture.md` 参照)。depgraph ドメインで言う `Project` (旧
  `Component`) とは別の概念。
- MCP ツールは登場時点で `add_component` という名前だったが v4.1.0 で
  `add_project` にリネームされた。古いドキュメントや会話で `component`
  と書かれているものは現 `Project` を指す。
