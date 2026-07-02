---
# Quy tắc mặc định tích hợp của dự án (Phiên bản bảo mật Phase 1)
#
# Chỉ đặt các ràng buộc mặc định "có thể kiểm tra bằng máy + ít tranh cãi" ở đây. Các sở thích thẩm mỹ phi máy móc (như xu hướng phong cách)
# Hiện tại vẫn do writer.md / editor.md gánh vác, đợi sau khi Phase 1.5 (F1 kiểm tra bằng tay xác minh
# năng lực ràng buộc của working_memory) mới quyết định có chuyển vào tệp này hay không.
#
# Người dùng có thể ghi đè các trường thông thường trong thư mục ./.ainovel/rules/ hoặc ~/.ainovel/rules/ (bất kỳ tệp .md nào dưới đó);
# fatigue_words được gộp theo từ, cùng một từ sẽ bị ghi đè ngưỡng bởi nguồn gần hơn.
# Ngữ nghĩa chi tiết của các trường tham khảo rules.md.example ở thư mục gốc của dự án.

# Phạm vi số chữ của chương: độ lệch <20% cảnh báo; ≥20% lỗi.
chapter_words: 3000-6000

# Danh sách đen cụm từ: xuất hiện ≥1 lần sẽ báo lỗi (error). Checker thực hiện khớp chuỗi con theo nghĩa đen, không có ký tự đại diện,
# do đó chỉ đặt các câu thoại AI "chuỗi cố định độ dài" (ít tranh cãi); các mô thức có chứa biến số (như "không phải X mà là Y")
# khớp theo nghĩa đen sẽ không bắt được, thuộc về lớp ngữ nghĩa của anti-ai-tone.md.
# Dấu gạch ngang "——" là hợp lệ khi cuộc trò chuyện bị gián đoạn, có tranh cãi, không đưa vào mặc định tích hợp, để lại cho người dùng tự cấu hình trong ./.ainovel/rules/.
forbidden_phrases:
  - "ở một mức độ nào đó"
  - "điều đáng chú ý là"
  - "không hiểu vì sao"
  - "ngũ vị tạp trần"

# Hạn chế mềm từ mệt mỏi: commit_chapter sẽ kiểm tra số lần xuất hiện trong mỗi chương, vượt quá ngưỡng sẽ báo cảnh báo (warning).
# Đây là các từ thường bị sử dụng quá mức trong truyện mạng/tiểu thuyết, anti-ai-tone.md có các gợi ý ngữ nghĩa cùng hướng - tín hiệu hai nguồn nhất quán.
# Sáu mục cuối cùng (giống như một/im lặng/không nói gì/vài nhịp thở/một nhịp thở/vài nhịp thở) đến từ thực chứng chạy dài 196 chương: các câu sáo rỗng truyền thống đã bị
# quét sạch bởi các quy tắc trên, nhưng mô hình lại quay sang sử dụng các "từ nhịp" này trung bình 5-7 lần mỗi chương; nới lỏng ngưỡng để dung thứ cho việc sử dụng bình thường.
fatigue_words:
  không khỏi: 1
  lại có thể: 1
  như thể: 2
  ngoài ra: 1
  tuy nhiên: 2
  một tia: 2
  một thoáng: 2
  một làn: 2
  như thể: 1
  không khỏi: 1
  giống như một: 3
  im lặng: 2
  không nói gì: 2
  vài nhịp thở: 3
  một nhịp thở: 3
  vài nhịp thở: 2
---