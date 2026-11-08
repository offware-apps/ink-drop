package io.github.offwareapps.inkdrop;

import android.content.ContentResolver;
import android.content.Context;
import android.content.SharedPreferences;
import android.database.Cursor;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Uri;
import android.provider.DocumentsContract;
import android.provider.DocumentsContract.Document;
import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.InetAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Date;
import java.util.List;
import java.util.Locale;

// Refresher makes books, for the screen and for the background refresh alike:
// it runs the stages the way the laptop pipe does, copies each book into the
// picked folder, and writes what happened to the log.
final class Refresher {
  // The log keeps its last MAX_LOG lines, in a file so it survives a restart.
  private static final int MAX_LOG = 600;
  // One run at a time, whether the screen or the background started it.
  private static final Object RUN = new Object();
  private static final Object LOG = new Object();

  private final Context ctx;
  private final SharedPreferences prefs;
  private final File logFile;
  private volatile boolean cancelled;
  private String[] network;
  private static volatile boolean active;
  private static volatile long runs;
  private volatile Process running;

  Refresher(Context ctx) {
    this.ctx = ctx.getApplicationContext();
    prefs = ctx.getSharedPreferences("ink-drop", Context.MODE_PRIVATE);
    logFile = new File(ctx.getFilesDir(), "log.txt");
  }

  Uri folder() {
    String f = prefs.getString("folder", null);
    return f == null ? null : Uri.parse(f);
  }

  // feeds returns the saved feed list.
  List<Feeds.Feed> feeds() {
    return new Feeds(new File(ctx.getFilesDir(), "feeds.opml")).list;
  }

  // cancel stops the run under way, the background job's when Android stops it:
  // the stage running is killed, and no further feed starts.
  void cancel() {
    cancelled = true;
    Process p = running;
    if (p != null) p.destroy();
  }

  // refresh makes one book per new article of each feed, or one of them all,
  // in a folder named after the feed, deletes the books past what the feed
  // keeps, and returns one sentence saying what came of it. A feed that fails
  // leaves the others to run.
  String refresh(List<Feeds.Feed> list, String why) {
    return run("refresh " + list.size() + " feeds, " + why, failed -> {
      int made = 0;
      // Each run starts after the last feed the run before finished, so a run
      // Android stops before the end reaches every feed in turn.
      int start = list.isEmpty() ? 0 : prefs.getInt("next-feed", 0) % list.size();
      for (int k = 0; k < list.size(); k++) {
        if (cancelled) break;
        int i = (start + k) % list.size();
        // A feed removed while this run waited or ran is left alone, and one
        // changed meanwhile runs as it is now.
        List<Feeds.Feed> now = feeds();
        Feeds.Feed f = now.stream().filter(g -> g.url.equals(list.get(i).url)).findFirst().orElse(null);
        if (f == null) continue;
        try {
          File dir = feedDir(f.url);
          adopt(dir);
          List<String> args = new ArrayList<>(List.of("-to", dir.getPath()));
          if (!f.together) args.add("-each");
          if (!f.mute.isEmpty()) args.addAll(List.of("-mute", f.mute));
          if (!f.only.isEmpty()) args.addAll(List.of("-only", f.only));
          stage("inkfeed", List.of(f.url), failed, args.toArray(new String[0]));
          made += bind(dir, shelf(f.title), Feeds.host(f.url), failed);
        } catch (Exception e) {
          log("feed " + f.title + ": " + e);
          failed.add(f.url);
        }
        // Run whether or not the feed answered: a book's days are counted from
        // when it arrived.
        if (!cancelled) {
          try {
            prune(f, now);
          } catch (Exception e) {
            log("delete old books of " + f.title + ": " + e);
          }
        }
        // Past this feed even when stopped in it: ink-feed kept what it wrote,
        // and a feed too slow for one run must not hold every other one back.
        prefs.edit().putInt("next-feed", (i + 1) % list.size()).apply();
      }
      return made;
    });
  }

  // save makes a book of the page at url, in the Pages folder.
  String save(String url, String why) {
    return run("save " + url + ", " + why, failed -> {
      File dir = new File(work(), "pages");
      List<String> fetched = stage("inkfetch", List.of(url), failed, "-to", dir.getPath());
      // Every page still as fetched, this one and any an earlier share left when
      // the app was stopped between the two stages, is cleaned before binding.
      List<String> raw = fetchedOnly(dir);
      List<String> read;
      try {
        read = stage("inkreadable", raw, failed, "-to", dir.getPath());
      } catch (IOException e) {
        // This share's page goes; an earlier share's waits for the next one.
        for (String item : fetched) forget(new File(item));
        throw e;
      }
      // A page ink-readable finds no article in is no book, as on the laptop.
      for (String item : raw) if (!read.contains(item)) forget(new File(item));
      return bind(dir, "Pages", null, failed);
    });
  }

  private interface Job {
    int make(List<String> failed) throws Exception;
  }

  // run does one job at a time, logs it, and returns one sentence saying what
  // came of it.
  private String run(String what, Job job) {
    synchronized (RUN) {
      active = true;
      try {
        return runLocked(what, job);
      } finally {
        active = false;
      }
    }
  }

  private String runLocked(String what, Job job) {
    if (cancelled) return "Stopped before it started.";
    // The network's settings, read once at the run's first download.
    network = null;
    long t0 = System.currentTimeMillis();
    log(what);
    String result;
    try {
      if (folder() == null) throw new IOException("no folder picked");
      // A folder gone or no longer granted would take every page fetched and
      // keep none: nothing is fetched until it can be read again.
      if (!readable()) throw new IOException("the picked folder can no longer be read");
      List<String> failed = new ArrayList<>();
      int made = job.make(failed);
      String took = String.format(Locale.ROOT, "%.1f s", (System.currentTimeMillis() - t0) / 1000.0);
      if (made == 1) result = "Saved 1 book in " + took + ".";
      else if (made > 1) result = "Saved " + made + " books in " + took + ".";
      else result = failed.isEmpty() ? "Nothing new since the last refresh." : "Nothing saved.";
      if (!failed.isEmpty()) result += " " + failed.size() + (failed.size() == 1 ? " address" : " addresses") + " failed: see Logs.";
      if (cancelled) result += " Stopped before the end; the rest comes next time.";
    } catch (Exception e) {
      log("failed: " + e);
      result = "That did not work. See Logs for why.";
    }
    log("── " + result);
    prefs.edit().putLong("last-run", System.currentTimeMillis()).putString("last-result", result).apply();
    runs++;
    return result;
  }

  // readable reports whether the picked folder can still be read.
  boolean readable() {
    Uri tree = folder();
    if (tree == null) return false;
    try (Cursor c = ctx.getContentResolver().query(DocumentsContract.buildDocumentUriUsingTree(tree, DocumentsContract.getTreeDocumentId(tree)),
        new String[] {Document.COLUMN_DOCUMENT_ID}, null, null, null)) {
      return c != null && c.moveToFirst();
    } catch (RuntimeException e) {
      return false;
    }
  }

  // busy reports whether a run is under way.
  static boolean busy() {
    return active;
  }

  // runs counts the runs finished, so a walk of the folder can tell whether
  // one finished while it walked.
  static long runs() {
    return runs;
  }

  private File work() {
    return new File(ctx.getFilesDir(), "work");
  }

  // feedDir returns the folder one feed's stage writes in, its record of seen
  // entries included, named by the feed's address.
  private File feedDir(String url) throws IOException {
    try {
      byte[] h = java.security.MessageDigest.getInstance("SHA-256").digest(url.getBytes(StandardCharsets.UTF_8));
      StringBuilder hex = new StringBuilder();
      for (int i = 0; i < 8; i++) hex.append(String.format("%02x", h[i]));
      return new File(work(), "feeds/" + hex);
    } catch (java.security.NoSuchAlgorithmException e) {
      throw new IOException(e);
    }
  }

  // bind makes a book of each page waiting in dir and copies it into the shelf
  // folder. A page leaves dir only once its book is in the picked folder, so
  // a run that fails part way, the folder gone or the job stopped, leaves the
  // rest for the next run. A page ink-epub refuses is dropped, since it never
  // will be a book.
  private int bind(File dir, String shelf, String site, List<String> failed) throws Exception {
    // A stage killed mid-write leaves its temporary files behind.
    for (File d : new File[] {dir, new File(dir, ".ink"), new File(dir, ".ink/feeds"), new File(dir, "books"), new File(dir, "books/.ink")}) {
      File[] tmp = d.listFiles((x, n) -> n.startsWith(".ink-tmp"));
      if (tmp != null) for (File t : tmp) t.delete();
    }
    File[] pages = dir.listFiles((d, n) -> n.endsWith(".html") || n.endsWith(".pdf"));
    if (pages == null) return 0;
    java.util.Arrays.sort(pages);
    File books = new File(dir, "books");
    int made = 0;
    for (File page : pages) {
      if (cancelled) break;
      List<String> refused = new ArrayList<>();
      List<String> out;
      try {
        out = stage("inkepub", List.of(page.getPath()), refused, epubArgs(books));
      } catch (IOException e) {
        if (cancelled) throw e;
        // A full disk or a stage killed on this page: it waits for the next
        // run, and the pages after it go on.
        log("book " + page.getName() + ": " + e);
        failed.add(page.getName());
        continue;
      }
      if (out.isEmpty()) {
        failed.addAll(refused);
        forget(page);
        continue;
      }
      try {
        for (String b : out) {
          File book = new File(b);
          if (copy(book, shelf, site)) made++;
          forget(book);
        }
        forget(page);
      } catch (IOException e) {
        // The page waits for the next run; the pages after it go on.
        log("copy " + page.getName() + ": " + e);
        failed.add(page.getName());
      }
    }
    return made;
  }

  // epubArgs returns ink-epub's flags: pictures no wider than the screen's
  // short side, and grey unless colour was picked.
  private String[] epubArgs(File to) {
    android.util.DisplayMetrics m = ctx.getResources().getDisplayMetrics();
    List<String> args = new ArrayList<>(List.of("-to", to.getPath(), "-max-width", Integer.toString(Math.min(m.widthPixels, m.heightPixels))));
    if (!prefs.getBoolean("colour", false)) args.add("-grey");
    return args.toArray(new String[0]);
  }

  // prune deletes the books in a feed's folder past what the feed keeps. A
  // folder two feeds share keeps what the looser of them keeps.
  private void prune(Feeds.Feed f, List<Feeds.Feed> all) throws IOException {
    String shelf = shelf(f.title);
    int days = f.keepDays, most = f.keepMost;
    for (Feeds.Feed g : all) {
      if (g == f || !sameShelf(shelf(g.title), shelf)) continue;
      days = looser(days, g.keepDays);
      most = looser(most, g.keepMost);
    }
    if (days == 0 && most == 0) return;
    Uri dir = find(null, shelf);
    if (dir == null) return;
    List<String[]> books = new ArrayList<>();
    List<String> dirs = new ArrayList<>();
    collect(DocumentsContract.getDocumentId(dir), 1, books, dirs);
    books.sort((a, b) -> Long.compare(Long.parseLong(b[1]), Long.parseLong(a[1])));
    long[] times = new long[books.size()];
    for (int i = 0; i < times.length; i++) times[i] = Long.parseLong(books.get(i)[1]);
    ContentResolver cr = ctx.getContentResolver();
    int gone = 0;
    for (int i : past(times, System.currentTimeMillis(), days, most)) {
      if (DocumentsContract.deleteDocument(cr, DocumentsContract.buildDocumentUriUsingTree(folder(), books.get(i)[0]))) gone++;
    }
    if (gone == 0) return;
    // A section's folder left empty goes too, the deepest first.
    for (int i = dirs.size() - 1; i >= 0; i--) {
      if (isEmpty(dirs.get(i))) DocumentsContract.deleteDocument(cr, DocumentsContract.buildDocumentUriUsingTree(folder(), dirs.get(i)));
    }
    log("deleted " + gone + " of the " + books.size() + " books in " + shelf + ", keeping "
        + (days == 0 ? "" : days + (days == 1 ? " day" : " days")) + (days != 0 && most != 0 ? ", " : "") + (most == 0 ? "" : "the newest " + most));
  }

  // looser returns the looser of two limits, 0 meaning none.
  static int looser(int a, int b) {
    return a == 0 || b == 0 ? 0 : Math.max(a, b);
  }

  // past returns which of the books, their times newest first, a feed keeping
  // days days and its newest most no longer keeps; 0 keeps any. A book whose
  // time the folder does not give, 0, is never too old.
  static List<Integer> past(long[] newestFirst, long now, int days, int most) {
    List<Integer> out = new ArrayList<>();
    for (int i = 0; i < newestFirst.length; i++) {
      boolean old = days > 0 && newestFirst[i] > 0 && newestFirst[i] < now - days * 86_400_000L;
      if (old || most > 0 && i >= most) out.add(i);
    }
    return out;
  }

  // collect adds each book under the folder id, its document id and time, and
  // each folder inside it, keeping to the folders the Books tab counts.
  private void collect(String id, int depth, List<String[]> books, List<String> dirs) {
    String[] cols = {Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE, Document.COLUMN_LAST_MODIFIED};
    List<String> inside = new ArrayList<>();
    try (Cursor c = ctx.getContentResolver().query(DocumentsContract.buildChildDocumentsUriUsingTree(folder(), id), cols, null, null, null)) {
      while (c != null && c.moveToNext()) {
        String child = c.getString(0), name = c.getString(1);
        if (underLaptop(child) || name.startsWith(".")) continue;
        if (Document.MIME_TYPE_DIR.equals(c.getString(2))) {
          if (depth < 4) inside.add(child);
        } else if (name.endsWith(".epub") || name.endsWith(".pdf")) {
          books.add(new String[] {child, Long.toString(c.getLong(3))});
        }
      }
    }
    for (String d : inside) {
      dirs.add(d);
      collect(d, depth + 1, books, dirs);
    }
  }

  // fetchedOnly returns the pages in dir that ink-fetch wrote and no stage
  // cleaned since, by their sidecar's stage.
  private static List<String> fetchedOnly(File dir) {
    List<String> out = new ArrayList<>();
    File[] pages = dir.listFiles((d, n) -> n.endsWith(".html") || n.endsWith(".pdf"));
    if (pages == null) return out;
    java.util.Arrays.sort(pages);
    for (File p : pages) {
      File side = new File(new File(dir, ".ink"), p.getName() + ".json");
      try {
        String stage = new org.json.JSONObject(new String(Files.readAllBytes(side.toPath()), StandardCharsets.UTF_8)).optString("stage");
        if (stage.equals("ink-fetch")) out.add(p.getPath());
      } catch (IOException | org.json.JSONException e) {
        // A page with no readable sidecar is no stage's: bind refuses it.
      }
    }
    return out;
  }

  // adopt gives a feed's new folder the record of seen entries every feed
  // shared before each had its own, so no article already a book comes again.
  // The record names each feed by its address, so the others' entries in it
  // are never read.
  private void adopt(File dir) throws IOException {
    File seen = new File(dir, ".ink/feeds");
    File[] shared = new File(work(), ".ink/feeds").listFiles();
    if (seen.exists() || shared == null) return;
    if (!seen.mkdirs()) throw new IOException("could not make " + seen);
    for (File f : shared) Files.copy(f.toPath(), new File(seen, f.getName()).toPath());
  }

  // forget deletes a page or a book and its sidecar.
  private static void forget(File f) {
    new File(new File(f.getParentFile(), ".ink"), f.getName() + ".json").delete();
    f.delete();
  }

  // shelf returns the folder name for a feed: its title up to a " - " or a
  // " | ", so "Le Monde.fr - Actualités…" files under "Le Monde.fr".
  static String shelf(String title) {
    String s = title;
    for (String cut : new String[] {" - ", " | ", " – ", " — "}) {
      int i = s.indexOf(cut);
      if (i >= 3) s = s.substring(0, i);
    }
    // Then no character a folder name cannot hold.
    s = s.replaceAll("[\\p{Cntrl}/\\\\:*?\"<>|]", " ");
    s = s.replaceAll("\\s+", " ").trim();
    if (s.length() > 60) s = s.substring(0, 60).trim();
    // Laptop is the folder Syncthing fills from the laptop and Pages holds shared
    // pages: neither is ever a feed's folder, so a feed's cleanup never reaches them.
    if (s.equalsIgnoreCase("Laptop") || s.equalsIgnoreCase("Pages") || kept(s)) s += " feed";
    return s.isEmpty() || s.startsWith(".") ? "Feed" : s;
  }

  // dir returns the folder named name inside the one at parent, the picked
  // folder when parent is null, made if missing.
  private Uri dir(Uri parent, String name) throws IOException {
    Uri found = find(parent, name);
    if (found != null) return found;
    Uri tree = folder();
    String id = parent == null ? DocumentsContract.getTreeDocumentId(tree) : DocumentsContract.getDocumentId(parent);
    Uri made = DocumentsContract.createDocument(ctx.getContentResolver(), DocumentsContract.buildDocumentUriUsingTree(tree, id), Document.MIME_TYPE_DIR, name);
    if (made == null) throw new IOException("could not make the folder " + name);
    return made;
  }

  // find returns the folder named name inside the one at parent, the picked
  // folder when parent is null, or null when there is none.
  private Uri find(Uri parent, String name) {
    Uri tree = folder();
    String id = parent == null ? DocumentsContract.getTreeDocumentId(tree) : DocumentsContract.getDocumentId(parent);
    Uri children = DocumentsContract.buildChildDocumentsUriUsingTree(tree, id);
    String[] cols = {Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE};
    try (Cursor c = ctx.getContentResolver().query(children, cols, null, null, null)) {
      while (c != null && c.moveToNext()) {
        // Shared storage folds case, so "Tech" and "tech" are one folder.
        if (name.equalsIgnoreCase(c.getString(1)) && Document.MIME_TYPE_DIR.equals(c.getString(2))) {
          return DocumentsContract.buildDocumentUriUsingTree(tree, c.getString(0));
        }
      }
    }
    return null;
  }

  // deleteShelf deletes every book in a feed's folder, then each folder left
  // empty, and reports whether every book is gone; a file that is no book stays.
  // Laptop and Pages are refused, whoever asks. It waits for a run under way.
  boolean deleteShelf(String shelf) {
    if (shelf.isEmpty() || shelf.equalsIgnoreCase("Laptop") || shelf.equalsIgnoreCase("Pages") || kept(shelf)) return false;
    synchronized (RUN) {
      try {
        Uri dir = find(null, shelf);
        boolean gone = dir == null || deleteBooks(DocumentsContract.getDocumentId(dir), 1);
        log("deleted the books of " + shelf + ": " + gone);
        return gone;
      } catch (Exception e) {
        log("delete " + shelf + ": " + e);
        return false;
      }
    }
  }

  // deleteBooks deletes the books under the folder id, and the folder once
  // empty, and reports whether every book went.
  // It keeps to the folders the Books tab counts: four deep, none hidden.
  private boolean deleteBooks(String id, int depth) throws IOException {
    Uri tree = folder();
    ContentResolver cr = ctx.getContentResolver();
    String[] cols = {Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE};
    boolean all = true;
    int left = 0;
    try (Cursor c = cr.query(DocumentsContract.buildChildDocumentsUriUsingTree(tree, id), cols, null, null, null)) {
      while (c != null && c.moveToNext()) {
        String child = c.getString(0), name = c.getString(1);
        if (underLaptop(child)) {
          // Never deleted, and the books under it are not gone.
          left++;
          all = false;
        } else if (name.startsWith(".") || depth >= 4 && Document.MIME_TYPE_DIR.equals(c.getString(2))) {
          left++;
        } else if (Document.MIME_TYPE_DIR.equals(c.getString(2))) {
          all &= deleteBooks(child, depth + 1);
          left++;
        } else if (name.endsWith(".epub") || name.endsWith(".pdf")) {
          all &= DocumentsContract.deleteDocument(cr, DocumentsContract.buildDocumentUriUsingTree(tree, child));
        } else {
          left++;
        }
      }
    }
    // A folder holding only folders now emptied, or nothing, goes too.
    if (left == 0 || isEmpty(id)) DocumentsContract.deleteDocument(cr, DocumentsContract.buildDocumentUriUsingTree(tree, id));
    return all;
  }

  private boolean isEmpty(String id) {
    try (Cursor c = ctx.getContentResolver().query(DocumentsContract.buildChildDocumentsUriUsingTree(folder(), id),
        new String[] {Document.COLUMN_DOCUMENT_ID}, null, null, null)) {
      return c == null || !c.moveToFirst();
    }
  }

  // Favourites holds the books the user starred and Read later those set aside;
  // no feed's settings reach either.
  static final String FAVOURITES = "Favourites", READ_LATER = "Read later";

  // kept reports whether a folder name is one of the user's own, which no
  // feed files into and no feed's settings reach.
  static boolean kept(String shelf) {
    return shelf.equalsIgnoreCase(FAVOURITES) || shelf.equalsIgnoreCase(READ_LATER);
  }

  // keep moves the book at doc into the folder into, Favourites or Read later,
  // and reports whether it moved.
  boolean keep(String doc, String into) {
    if (underLaptop(doc) || doc.lastIndexOf('/') < 0) return false;
    synchronized (RUN) {
      try {
        return move(doc, dir(null, into), into) != null;
      } catch (IOException e) {
        log("move " + doc + " to " + into + ": " + e);
        return false;
      }
    }
  }

  // release moves a book out of Favourites or Read later back to the folder it
  // came from, where its feed's settings reach it again, and reports whether it
  // moved. Where that folder is not known or gone, its source tells it: its
  // feed's folder and section, or Pages.
  boolean release(String doc) {
    synchronized (RUN) {
      try {
        String from = prefs.getString("from:" + doc, null);
        Uri back = from == null ? null : DocumentsContract.buildDocumentUriUsingTree(folder(), from);
        if (back != null) {
          try (Cursor c = ctx.getContentResolver().query(back, new String[] {Document.COLUMN_DOCUMENT_ID}, null, null, null)) {
            if (c == null || !c.moveToFirst()) back = null;
          }
        }
        if (back == null) back = home(doc);
        String now = move(doc, back, "its folder");
        if (now == null) return false;
        prefs.edit().remove("from:" + now).apply();
        return true;
      } catch (IOException | RuntimeException e) {
        log("put back " + doc + ": " + e);
        return false;
      }
    }
  }

  // home returns the folder a book belongs in by its source, made if missing:
  // the folder of the feed on its site, in its section, else Pages.
  private Uri home(String doc) throws IOException {
    String source = prefs.getString("source:" + doc, "");
    if (!source.isEmpty()) {
      String host = Feeds.host(source);
      for (Feeds.Feed f : feeds()) {
        if (!Feeds.host(f.url).equals(host)) continue;
        Uri into = dir(null, shelf(f.title));
        String section = section(source);
        return section.isEmpty() ? into : dir(into, section);
      }
    }
    return dir(null, "Pages");
  }

  // read reports whether the book at doc was opened from the app.
  boolean read(String doc) {
    return prefs.contains("read:" + doc);
  }

  // move moves the book at doc into the folder at to, what is kept for it
  // going along, and returns its new document id, or null where it did not
  // move. The folder a book first left is kept as from:, for release.
  private String move(String doc, Uri to, String name) {
    active = true;
    try {
      ContentResolver cr = ctx.getContentResolver();
      Uri tree = folder();
      String parent = doc.substring(0, doc.lastIndexOf('/'));
      Uri moved = DocumentsContract.moveDocument(cr, DocumentsContract.buildDocumentUriUsingTree(tree, doc),
          DocumentsContract.buildDocumentUriUsingTree(tree, parent), to);
      if (moved == null) return null;
      String now = DocumentsContract.getDocumentId(moved);
      SharedPreferences.Editor e = prefs.edit();
      for (String k : KEPT) {
        String v = prefs.getString(k + doc, null);
        if (v != null) e.remove(k + doc).putString(k + now, v);
      }
      if (!prefs.contains("from:" + doc)) e.putString("from:" + now, parent);
      e.apply();
      log("moved " + doc + " to " + name);
      return now;
    } catch (Exception e) {
      log("move " + doc + " to " + name + ": " + e);
      return null;
    } finally {
      active = false;
      runs++;
    }
  }

  // What is kept per book, keyed by its document id.
  private static final String[] KEPT = {"title:", "source:", "published:", "author:", "from:", "read:"};

  // Path words that name no section: a site's own page kinds and languages.
  private static final java.util.Set<String> NOT_SECTIONS = java.util.Set.of(
      "article", "articles", "rss", "feed", "feeds", "titres", "blog", "news", "post", "posts",
      "p", "fr", "en", "de", "es", "it", "www", "amp", "live", "story", "stories", "index",
      // Laptop is Syncthing's name, never a section the app writes into.
      "laptop");

  // section returns the label of an article: the first word of its address's
  // path, "politique" in lemonde.fr/politique/article/…, written as words, or
  // "" where that word is a date, a number or a page kind.
  static String section(String source) {
    String path;
    try {
      java.net.URI u = new java.net.URI(source);
      // A refresh's one book, named after its feed, has no section.
      if (u.getFragment() != null && u.getFragment().startsWith("digest-")) return "";
      path = u.getPath();
    } catch (Exception e) {
      return "";
    }
    if (path == null) return "";
    String first = path.replaceFirst("^/+", "").split("/", 2)[0].toLowerCase(Locale.ROOT);
    if (first.length() < 3 || first.length() > 30 || first.contains(".") || !first.matches("[a-z][a-z-]*") || NOT_SECTIONS.contains(first)) return "";
    String words = first.replace('-', ' ').trim();
    return Character.toUpperCase(words.charAt(0)) + words.substring(1);
  }


  // published returns when the book at doc was published, as its feed says,
  // else the time given, when it arrived.
  long published(String doc, long arrived) {
    try {
      return Long.parseLong(prefs.getString("published:" + doc, Long.toString(arrived)));
    } catch (NumberFormatException e) {
      return arrived;
    }
  }

  // author returns the book's author as its feed names them, or "".
  String author(String doc) {
    return prefs.getString("author:" + doc, "");
  }

  // title returns the title the book at doc was made with, else its file name
  // in words; a book copied before titles were keyed by document is found by
  // its file name.
  String title(String doc, String file) {
    String s = file.replaceAll("\\.(epub|pdf)$", "").replace('-', ' ');
    s = s.isEmpty() ? file : Character.toUpperCase(s.charAt(0)) + s.substring(1);
    return prefs.getString("title:" + doc, prefs.getString("title:" + file, s));
  }

  // forgetTitles drops the titles and sources kept for books no longer in the
  // folder.
  void forgetTitles(java.util.Set<String> docs, long runsBefore) {
    // A run copying books meanwhile writes titles the walk did not see.
    if (active || runs != runsBefore) return;
    SharedPreferences.Editor e = prefs.edit();
    int n = 0;
    for (String k : prefs.getAll().keySet()) {
      // A key naming a document holds a colon; one naming a bare file is older.
      String doc = null;
      for (String p : KEPT) if (k.startsWith(p)) doc = k.substring(p.length());
      if (doc != null && doc.contains(":") && !docs.contains(doc)) {
        e.remove(k);
        n++;
      }
    }
    if (n > 0) e.apply();
  }

  // stage runs one stage program with the lines on its input and returns the
  // paths it printed. A line the stage could not handle, exit 1, is added to
  // failed, and the rest go on.
  private List<String> stage(String name, List<String> in, List<String> failed, String... args)
      throws IOException, InterruptedException {
    List<String> cmd = new ArrayList<>();
    cmd.add(ctx.getApplicationInfo().nativeLibraryDir + "/lib" + name + ".so");
    for (String a : args) cmd.add(a);
    ProcessBuilder pb = new ProcessBuilder(cmd);
    // Any app on the tablet can share an address with ink-drop, so no download
    // may reach the tablet itself or the home network behind it. ink-epub
    // downloads nothing, and needs none of this.
    pb.environment().put("INK_PUBLIC_ONLY", "1");
    if (!name.equals("inkepub")) {
      if (network == null) network = new String[] {null, null, localPrefixes()};
      if (network[0] == null) {
        String[] dns = dnsServers();
        network[0] = dns[0];
        network[1] = dns[1];
      }
      pb.environment().put("INK_DNS", network[0]);
      if (!network[1].isEmpty()) pb.environment().put("INK_DOT", network[1]);
      pb.environment().put("INK_LOCAL", network[2]);
    }
    // Input from a file: a stage writing more than a pipe holds while its input
    // was still being written would wait on the app, and the app on it.
    File input = new File(ctx.getCacheDir(), name + ".in");
    Files.write(input.toPath(), in, StandardCharsets.UTF_8);
    pb.redirectInput(input);
    File err = new File(ctx.getCacheDir(), name + ".err");
    pb.redirectError(err);
    Process p = pb.start();
    running = p;
    if (cancelled) p.destroy();
    List<String> out = new ArrayList<>();
    try {
      BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8));
      for (String line; (line = r.readLine()) != null; ) if (!line.isEmpty()) out.add(line);
    } finally {
      running = null;
    }
    int code = p.waitFor();
    // A server's error page may carry any bytes; they are text to show, never a
    // reason for the run to fail.
    String text = new String(Files.readAllBytes(err.toPath()), StandardCharsets.UTF_8).trim();
    String[] lines = text.isEmpty() ? new String[0] : text.split("\n");
    String shown = lines.length <= 20 ? text : String.join("\n", java.util.Arrays.copyOf(lines, 20)) + "\n… " + (lines.length - 20) + " more lines";
    log(name + ": exit " + code + ", " + out.size() + " out" + (text.isEmpty() ? "" : "\n" + shown));
    if (cancelled) throw new IOException(name + " stopped");
    if (code > 1 || (code == 1 && text.isEmpty())) throw new IOException(name + " exited " + code);
    // Only "<stage>: <input>: <error>" names a failed input; other lines, an
    // image skipped, are notes.
    String cli = name.replaceFirst("^ink", "ink-");
    if (code == 1) for (String line : in) if (text.contains(cli + ": " + line + ": ")) failed.add(line);
    return out;
  }

  // dnsServers returns the current network's DNS servers, comma-separated, for
  // the stages, which cannot find them on Android by themselves, and the
  // Private DNS host when the tablet sets one, with its addresses as the servers.
  private String[] dnsServers() {
    ConnectivityManager cm = ctx.getSystemService(ConnectivityManager.class);
    LinkProperties lp = cm.getLinkProperties(cm.getActiveNetwork());
    List<String> out = new ArrayList<>();
    if (lp == null) return new String[] {"", ""};
    String dot = lp.isPrivateDnsActive() && lp.getPrivateDnsServerName() != null ? lp.getPrivateDnsServerName() : "";
    if (!dot.isEmpty()) {
      try {
        for (InetAddress a : InetAddress.getAllByName(dot)) out.add(a.getHostAddress());
      } catch (IOException e) {
        // Strict Private DNS never falls back to plain DNS: every lookup fails.
        log("Private DNS " + dot + ": " + e);
      }
      return new String[] {String.join(",", out), dot};
    }
    for (InetAddress a : lp.getDnsServers()) out.add(a.getHostAddress());
    return new String[] {String.join(",", out), ""};
  }

  // localPrefixes returns the prefixes of the network the tablet sits on,
  // comma-separated: its own addresses' and its routes' with no gateway. On
  // IPv6 these are global addresses the public-only check cannot tell apart.
  private String localPrefixes() {
    ConnectivityManager cm = ctx.getSystemService(ConnectivityManager.class);
    List<String> out = new ArrayList<>();
    for (android.net.Network n : cm.getAllNetworks()) {
      LinkProperties lp = cm.getLinkProperties(n);
      android.net.NetworkCapabilities caps = cm.getNetworkCapabilities(n);
      // A VPN's routes carry no gateway and may cover the whole internet.
      if (lp == null || caps == null || caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_VPN)) continue;
      for (android.net.LinkAddress a : lp.getLinkAddresses()) {
        String ip = a.getAddress().getHostAddress().replaceFirst("%.*$", "");
        out.add(ip + "/" + a.getPrefixLength());
        // A home is handed a /56 or wider and splits it into /64s: the
        // neighbouring networks, a server's for one, are home too.
        if (a.getAddress() instanceof java.net.Inet6Address && a.getPrefixLength() >= 56) out.add(ip + "/56");
      }
      for (android.net.RouteInfo r : lp.getRoutes()) {
        if (!r.hasGateway() && r.getDestination().getPrefixLength() > 0) out.add(r.getDestination().toString());
      }
    }
    return String.join(",", out);
  }

  // dropWork deletes a removed feed's working folder, the pages it left
  // waiting and its record of seen entries included.
  // With pagesOnly, the record of seen entries stays, and the pages waiting
  // become books in the feed's folder, since their entries are recorded and
  // never come again.
  void dropWork(String url, String title, boolean pagesOnly) {
    synchronized (RUN) {
      try {
        File dir = feedDir(url);
        if (pagesOnly) {
          // A page bind cannot copy, the folder gone for one, stays in dir:
          // the feed added again picks it up.
          if (readable()) {
            active = true;
            try {
              bind(dir, shelf(title), Feeds.host(url), new ArrayList<>());
            } catch (Exception e) {
              log("book the pages of " + title + ": " + e);
            } finally {
              active = false;
              runs++;
            }
          }
          return;
        }
        deleteTree(dir);
      } catch (IOException e) {
        log("drop work " + url + ": " + e);
      }
    }
  }

  private static void deleteTree(File f) {
    File[] in = f.listFiles();
    if (in != null) for (File c : in) deleteTree(c);
    f.delete();
  }

  // copy puts a finished book into the shelf folder, inside its section's folder
  // when it is on the feed's site and its address names one, and keeps its real title
  // from the stage's sidecar, since the file name lost its accents.
  // It reports false where the same article is already there.
  private boolean copy(File book, String shelf, String site) throws IOException {
    String title = "", source = "", published = "", author = "";
    File side = new File(new File(book.getParentFile(), ".ink"), book.getName() + ".json");
    try (InputStream in = new FileInputStream(side)) {
      org.json.JSONObject m = new org.json.JSONObject(new String(in.readAllBytes(), StandardCharsets.UTF_8));
      title = m.optString("title");
      source = m.optString("source");
      published = m.optString("published");
      author = m.optString("author");
    } catch (IOException | org.json.JSONException e) {
      log("no sidecar for " + book.getName() + ": " + e);
    }
    // A section only from the feed's own site: an aggregator's links name
    // other sites' paths, a user's name on GitHub for one.
    String section = site != null && Feeds.host(source).equals(site) ? section(source) : "";
    Uri into = dir(null, shelf);
    if (!section.isEmpty()) into = dir(into, section);
    ContentResolver cr = ctx.getContentResolver();
    String type = book.getName().endsWith(".pdf") ? "application/pdf" : "application/epub+zip";
    // A name another book holds gets the source's hash, since the provider
    // numbers a repeated name only up to 32; a copy already there under that
    // name is the same article, and is not made twice.
    String name = book.getName();
    String plain = childId(into, name), replaced = null;
    if (plain != null && source.equals(prefs.getString("source:" + plain, null))) {
      // A page shared again is saved as it is now, the old book deleted once
      // the new one is whole; a feed's entry is the same article, already there.
      if (!shelf.equals("Pages")) {
        log("already copied: " + shelf + "/" + name);
        return false;
      }
      replaced = plain;
      plain = null;
    }
    if (plain != null) {
      int dot = name.lastIndexOf('.');
      name = name.substring(0, dot) + "-" + hash(source) + name.substring(dot);
      String there = childId(into, name);
      if (there != null && source.equals(prefs.getString("source:" + there, null))) {
        log("already copied: " + shelf + "/" + name);
        return false;
      }
      // A different article under this name too: the provider numbers it.
    }
    Uri doc = DocumentsContract.createDocument(cr, into, type, name);
    if (doc == null) throw new IOException("could not make " + name + " in " + shelf);
    try (InputStream in = new FileInputStream(book); OutputStream out = cr.openOutputStream(doc)) {
      in.transferTo(out);
    } catch (IOException e) {
      // A half-written book would sit beside the whole one the next run makes.
      DocumentsContract.deleteDocument(cr, doc);
      throw e;
    }
    if (replaced != null) {
      // The provider named the new book "name (1)" beside the old one.
      try {
        DocumentsContract.deleteDocument(cr, DocumentsContract.buildDocumentUriUsingTree(folder(), replaced));
        Uri renamed = DocumentsContract.renameDocument(cr, doc, name);
        if (renamed != null) doc = renamed;
      } catch (IOException | RuntimeException e) {
        log("rename " + name + ": " + e);
      }
    }
    // Keyed by the copy's own document: two books may share a file name.
    SharedPreferences.Editor kept = prefs.edit().putString("source:" + DocumentsContract.getDocumentId(doc), source);
    if (!title.isEmpty()) kept.putString("title:" + DocumentsContract.getDocumentId(doc), title);
    if (!author.isEmpty()) kept.putString("author:" + DocumentsContract.getDocumentId(doc), author);
    try {
      if (!published.isEmpty()) kept.putString("published:" + DocumentsContract.getDocumentId(doc), Long.toString(java.time.Instant.parse(published).toEpochMilli()));
    } catch (java.time.format.DateTimeParseException e) {
      log("no date for " + name + ": " + published);
    }
    kept.apply();
    log("copied " + shelf + (section.isEmpty() ? "" : "/" + section) + "/" + name + " (" + book.length() + " bytes)");
    return true;
  }

  // sameShelf reports whether two folder names are one folder: shared
  // storage folds case, so "World news" and "World News" are.
  static boolean sameShelf(String a, String b) {
    return a != null && b != null && a.equalsIgnoreCase(b);
  }

  // underLaptop reports whether the document id has a folder named Laptop on
  // its way: Syncthing's, whichever folder above it was picked. A feed's
  // section of that name is held the same way, safety before convenience.
  static boolean underLaptop(String id) {
    for (String part : id.substring(id.indexOf(':') + 1).split("/")) {
      if (part.equalsIgnoreCase("laptop")) return true;
    }
    return false;
  }

  // childId returns the document id of the document named name in the folder
  // at parent, or null when it holds none.
  private String childId(Uri parent, String name) {
    Uri kids = DocumentsContract.buildChildDocumentsUriUsingTree(folder(), DocumentsContract.getDocumentId(parent));
    try (Cursor c = ctx.getContentResolver().query(kids, new String[] {Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME}, null, null, null)) {
      while (c != null && c.moveToNext()) if (name.equalsIgnoreCase(c.getString(1))) return c.getString(0);
    }
    return null;
  }

  // hash returns 16 hex digits of the SHA-256 of s.
  static String hash(String s) {
    try {
      byte[] h = java.security.MessageDigest.getInstance("SHA-256").digest(s.getBytes(StandardCharsets.UTF_8));
      StringBuilder hex = new StringBuilder();
      for (int i = 0; i < 8; i++) hex.append(String.format("%02x", h[i]));
      return hex.toString();
    } catch (java.security.NoSuchAlgorithmException e) {
      throw new IllegalStateException(e);
    }
  }

  // log adds a timestamped line to the log file. A line opening on "── " is a
  // run's summary, which the Logs tab shows as a heading.
  void log(String line) {
    synchronized (LOG) {
      String time = new SimpleDateFormat("d MMM HH:mm:ss", Locale.getDefault()).format(new Date());
      List<String> lines = logLines();
      for (String l : line.split("\n")) {
        lines.add(l.startsWith("── ") ? "── " + time + "  ·  " + l.substring(3) : time + "  " + l.trim());
      }
      if (lines.size() > MAX_LOG) lines = new ArrayList<>(lines.subList(lines.size() - MAX_LOG, lines.size()));
      try {
        Files.write(logFile.toPath(), lines, StandardCharsets.UTF_8);
      } catch (IOException e) {
        // The log is a help, never a reason for a run to fail.
      }
    }
  }

  List<String> logLines() {
    synchronized (LOG) {
      try {
        return new ArrayList<>(Files.readAllLines(logFile.toPath(), StandardCharsets.UTF_8));
      } catch (IOException e) {
        return new ArrayList<>();
      }
    }
  }

  void clearLog() {
    synchronized (LOG) {
      logFile.delete();
    }
  }
}
